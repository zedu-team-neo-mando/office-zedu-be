package office

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/utility"
	"gorm.io/gorm"
)

// Service holds the dependencies for the office domain.
type Service struct {
	Db     *storage.Database
	Logger *utility.Logger
}

// New builds the office service.
func New(db *storage.Database, logger *utility.Logger) *Service {
	return &Service{Db: db, Logger: logger}
}

// ---------------------------------------------------------------------------
// Room identifiers
// ---------------------------------------------------------------------------

// EncodeRoomID builds the opaque composite id used by every office route.
// Shared rooms without a reference (lobby, reception, lounge) are just the
// kind; everything else is "kind:ref".
func EncodeRoomID(kind, ref string) string {
	if ref == "" {
		return kind
	}
	return fmt.Sprintf("%s:%s", kind, ref)
}

// ParseRoomID turns an opaque room id back into its kind and reference. It
// accepts both the decoded ("channel:ch_1") and the URL encoded
// ("channel%3Ach_1") form, because clients may send either.
func ParseRoomID(roomID string) (models.OfficeRoomRef, error) {
	decoded, err := url.PathUnescape(strings.TrimSpace(roomID))
	if err != nil {
		decoded = strings.TrimSpace(roomID)
	}

	if decoded == "" {
		return models.OfficeRoomRef{}, errors.New("room id is required")
	}

	kind, ref, found := strings.Cut(decoded, ":")
	switch kind {
	case models.OfficeRoomKindLobby, models.OfficeRoomKindReception, models.OfficeRoomKindLounge:
		return models.OfficeRoomRef{Kind: kind}, nil
	case models.OfficeRoomKindMeeting, models.OfficeRoomKindFocus:
		if !found || ref == "" {
			return models.OfficeRoomRef{}, fmt.Errorf("room id %q is missing its reference", roomID)
		}
		return models.OfficeRoomRef{Kind: kind, Ref: ref}, nil
	case models.OfficeRoomKindChannel, models.OfficeRoomKindPersonal:
		if !found || ref == "" {
			return models.OfficeRoomRef{}, fmt.Errorf("room id %q is missing its reference", roomID)
		}
		return models.OfficeRoomRef{Kind: kind, Ref: ref}, nil
	case models.OfficeRoomKindArea:
		if !found {
			return models.OfficeRoomRef{}, fmt.Errorf("room id %q is missing its reference", roomID)
		}
		if _, _, ok := ParseAreaRef(ref); !ok {
			return models.OfficeRoomRef{}, fmt.Errorf("room id %q is not a valid area reference", roomID)
		}
		return models.OfficeRoomRef{Kind: kind, Ref: ref}, nil
	default:
		return models.OfficeRoomRef{}, fmt.Errorf("unknown room kind %q", kind)
	}
}

// IsSharedRoom reports whether the room is shared by the whole organisation
// (as opposed to a personal office).
func IsSharedRoom(ref models.OfficeRoomRef) bool {
	return ref.Kind != models.OfficeRoomKindPersonal
}

// RoomFloorIndex is the floor a room sits on when nothing more precise is
// known. Shared rooms live on the general floor and an area room carries its
// floor in its reference. A private office lives on its owner's floor, which
// only the floor assignment knows, so this is the top member floor fallback.
func RoomFloorIndex(ref models.OfficeRoomRef) int {
	switch ref.Kind {
	case models.OfficeRoomKindArea:
		if floor, _, ok := ParseAreaRef(ref.Ref); ok {
			return floor
		}
		return models.OfficeFloorMember
	case models.OfficeRoomKindPersonal:
		return models.OfficeFloorMember
	default:
		return models.OfficeFloorGeneral
	}
}

// IsEnabled reports whether the organisation has The Office switched on.
//
// The setting lives on organisations.office_settings and is read on every
// request, so enabling or disabling an organisation is a database change and
// never a deployment. An organisation that has never set the option keeps the
// office: the switch is there to take it away, not to hide it.
func (s *Service) IsEnabled(orgID string) (bool, error) {
	var enabled bool

	err := s.Db.Postgresql.Raw(
		"SELECT COALESCE((office_settings->>'enabled')::boolean, true) FROM organisations WHERE id = ?",
		orgID,
	).Scan(&enabled).Error
	if err != nil {
		return false, err
	}

	return enabled, nil
}

// Settings returns the organisation's office options. An organisation that
// has never set them keeps the office.
func (s *Service) Settings(orgID string) (models.OfficeSettings, error) {
	settings := models.OfficeSettings{Enabled: true}

	// The column is read into a NullString rather than a []byte: GORM cannot
	// drive a []byte destination on a raw query, it tries to read the column
	// into a single byte and the scan fails.
	var raw sql.NullString
	err := s.Db.Postgresql.Raw(
		"SELECT office_settings::text FROM organisations WHERE id = ?",
		orgID,
	).Scan(&raw).Error
	if err != nil {
		return models.OfficeSettings{}, err
	}

	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return settings, nil
	}

	values := map[string]any{}
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return models.OfficeSettings{}, err
	}

	if enabled, ok := values["enabled"].(bool); ok {
		settings.Enabled = enabled
	}
	if theme, ok := values["theme"].(string); ok {
		settings.Theme = theme
	}
	if capacity, ok := values["capacity"].(float64); ok {
		settings.Capacity = int(capacity)
	}

	return settings, nil
}

// UpdateSettings stores the organisation's office options atomically.
func (s *Service) UpdateSettings(orgID string, settings models.OfficeSettings) error {
	return s.Db.Postgresql.Transaction(func(tx *gorm.DB) error {
		var org models.Organisation
		if err := tx.Where("id = ?", orgID).First(&org).Error; err != nil {
			return err
		}

		// Merge with existing settings to avoid lost updates
		existing := org.OfficeSettings
		if settings.Enabled != existing.Enabled {
			existing.Enabled = settings.Enabled
		}

		return tx.Model(&models.Organisation{}).
			Where("id = ?", orgID).
			Update("office_settings", existing).Error
	})
}

// ---------------------------------------------------------------------------
// Access control (PRD section 6.5)
// ---------------------------------------------------------------------------

// Access describes what a requester may do inside a room.
type Access struct {
	CanEnter         bool
	CanEditFurniture bool
	CanPublish       bool
	CanReset         bool
}

// IsOrganisationAdmin reports whether the user holds the owner or administrator
// role in the organisation.
func (s *Service) IsOrganisationAdmin(orgID, userID string) bool {
	var membership models.OrgUserManagement
	if err := s.Db.Postgresql.Where("organisation_id = ? AND user_id = ?", orgID, userID).First(&membership).Error; err != nil {
		return false
	}
	if membership.RoleID == "" {
		return false
	}

	var role models.OrgRole
	if err := s.Db.Postgresql.Where("id = ?", membership.RoleID).First(&role).Error; err != nil {
		return false
	}

	if strings.EqualFold(role.Name, models.OrgRoleNameOwner) ||
		strings.EqualFold(role.Name, models.OrgRoleNameAdministrator) {
		return true
	}

	return models.OrgUserHasPermission(role.Permissions.PermissionList, models.PermManageOrganization)
}

// IsOrganisationMember reports whether the user belongs to the organisation.
func (s *Service) IsOrganisationMember(orgID, userID string) bool {
	var membership models.OrgUserManagement
	return s.Db.Postgresql.Where("organisation_id = ? AND user_id = ? AND is_deactivated = ?", orgID, userID, false).
		First(&membership).Error == nil
}

// CanAccess resolves the permissions of a requester for a room.
func (s *Service) CanAccess(orgID, userID string, ref models.OfficeRoomRef) (Access, error) {
	access := Access{}

	if !s.IsOrganisationMember(orgID, userID) {
		return access, nil
	}

	isAdmin := s.IsOrganisationAdmin(orgID, userID)

	// Personal offices: the owner and the people they talk to. An
	// administrator does not get a key to somebody's desk.
	if ref.Kind == models.OfficeRoomKindPersonal {
		if ref.Ref == userID {
			access.CanEnter = true
			access.CanEditFurniture = true
			return access, nil
		}
		guest, err := s.isDirectMessageParticipant(orgID, userID, ref.Ref)
		if err != nil {
			return access, err
		}
		access.CanEnter = guest
		return access, nil
	}

	// Shared rooms: everybody in the organisation may enter the building's
	// common areas. A public channel is open to the whole organisation; a
	// private channel needs a membership (administrators may enter any).
	if ref.Kind == models.OfficeRoomKindChannel {
		member, err := s.IsChannelMember(orgID, userID, ref.Ref)
		if err != nil {
			return access, err
		}

		var channel models.Channels
		err = s.Db.Postgresql.Select("id", "is_private").
			Where("id = ? AND organisation_id = ?", ref.Ref, orgID).
			Scopes(activeOfficeChannels).
			First(&channel).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return access, err
		}

		if !member && channel.IsPrivate && !isAdmin {
			return access, nil
		}
	}

	access.CanEnter = true
	access.CanEditFurniture = isAdmin
	access.CanPublish = isAdmin
	access.CanReset = isAdmin
	return access, nil
}

// isDirectMessageParticipant reports whether requesterID has a direct message
// (1:1 or group) with counterpartID inside the organisation.
func (s *Service) isDirectMessageParticipant(orgID, requesterID, counterpartID string) (bool, error) {
	var count int64

	// 1:1 direct messages: dm_channels holds one row per participant pair.
	err := s.Db.Postgresql.Model(&models.DmChannels{}).
		Where("org_id = ? AND user_id = ? AND participant_id = ?", orgID, requesterID, counterpartID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}

	// Group direct messages resolve through channel_participants.
	err = s.Db.Postgresql.Model(&models.ChannelParticipant{}).
		Where("org_id = ? AND user_id = ?", orgID, requesterID).
		Where("channel_id IN (?)", s.Db.Postgresql.Model(&models.ChannelParticipant{}).
			Select("channel_id").
			Where("org_id = ? AND user_id = ?", orgID, counterpartID)).
		Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// IsChannelMember reports whether the user belongs to a channel inside the
// organisation.
func (s *Service) IsChannelMember(orgID, userID, channelID string) (bool, error) {
	var count int64
	err := s.Db.Postgresql.Model(&models.UserChannels{}).
		Where("user_id = ? AND channels_id = ?", userID, channelID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}

	// The membership row alone is not enough: the channel has to belong to
	// the organisation the requester is in.
	var channelCount int64
	err = s.Db.Postgresql.Model(&models.Channels{}).
		Where("id = ? AND organisation_id = ?", channelID, orgID).
		Count(&channelCount).Error
	if err != nil {
		return false, err
	}

	return channelCount > 0, nil
}

// AccessSets holds the precomputed access information for a requester.
// It is built once per request and passed to directory building and other
// hot paths to avoid per-room round-trips.
type AccessSets struct {
	IsAdmin        bool
	ChannelIDs     map[string]struct{}
	PublicChannels map[string]struct{}
	DMParticipants map[string]struct{}
}

// AccessSets fetches the requester's access sets in three queries total.
func (s *Service) AccessSets(orgID, userID string) (AccessSets, error) {
	sets := AccessSets{
		ChannelIDs:     make(map[string]struct{}),
		PublicChannels: make(map[string]struct{}),
		DMParticipants: make(map[string]struct{}),
	}

	// 1. Admin check (1 query)
	var membership models.OrgUserManagement
	if err := s.Db.Postgresql.Where("organisation_id = ? AND user_id = ?", orgID, userID).First(&membership).Error; err == nil {
		if membership.RoleID != "" {
			var role models.OrgRole
			if err := s.Db.Postgresql.Where("id = ?", membership.RoleID).First(&role).Error; err == nil {
				if strings.EqualFold(role.Name, models.OrgRoleNameOwner) ||
					strings.EqualFold(role.Name, models.OrgRoleNameAdministrator) ||
					models.OrgUserHasPermission(role.Permissions.PermissionList, models.PermManageOrganization) {
					sets.IsAdmin = true
				}
			}
		}
	}

	// 2. Channel access (1 query). Every channel in the organisation is
	// considered: membership opens private channels, while every member may
	// walk into the public ones, exactly as the 2D chat allows.
	var channelRows []struct {
		ID        string
		IsPrivate bool
		Joined    bool
	}
	if err := s.Db.Postgresql.Table("channels").
		Select("channels.id, COALESCE(channels.is_private, false) AS is_private, (user_channels.user_id IS NOT NULL) AS joined").
		Joins("LEFT JOIN user_channels ON user_channels.channels_id = channels.id AND user_channels.user_id = ?", userID).
		Where("channels.organisation_id = ?", orgID).
		Scopes(activeOfficeChannels).
		Scan(&channelRows).Error; err == nil {
		for _, row := range channelRows {
			if row.Joined {
				sets.ChannelIDs[row.ID] = struct{}{}
			}
			if !row.IsPrivate {
				sets.PublicChannels[row.ID] = struct{}{}
			}
		}
	}

	// 3. DM participants (1 query for 1:1 + 1 for group)
	// 1:1 DMs
	var dmRows []struct{ ParticipantID *string }
	if err := s.Db.Postgresql.Model(&models.DmChannels{}).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Scan(&dmRows).Error; err == nil {
		for _, row := range dmRows {
			if row.ParticipantID != nil && *row.ParticipantID != "" {
				sets.DMParticipants[*row.ParticipantID] = struct{}{}
			}
		}
	}

	// Group DMs
	var participantRows []struct{ ChannelID string; UserID string }
	if err := s.Db.Postgresql.Model(&models.ChannelParticipant{}).
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Scan(&participantRows).Error; err == nil {
		for _, row := range participantRows {
			sets.DMParticipants[row.UserID] = struct{}{}
		}
	}

	return sets, nil
}

// CanAccessFromSets resolves access using precomputed sets (no DB queries).
func CanAccessFromSets(sets AccessSets, ref models.OfficeRoomRef, userID string) Access {
	access := Access{}

	// Personal offices: the owner and the people they talk to.
	if ref.Kind == models.OfficeRoomKindPersonal {
		if ref.Ref == userID {
			access.CanEnter = true
			access.CanEditFurniture = true
			return access
		}
		if _, ok := sets.DMParticipants[ref.Ref]; ok {
			access.CanEnter = true
			return access
		}
		// Admins do NOT get a key to somebody's desk.
		return access
	}

	// Shared rooms: everybody in the organisation may enter the building's
	// common areas. A public channel is open to the whole organisation; a
	// private channel needs a membership (administrators may enter any).
	if ref.Kind == models.OfficeRoomKindChannel {
		_, member := sets.ChannelIDs[ref.Ref]
		_, public := sets.PublicChannels[ref.Ref]
		if !member && !public && !sets.IsAdmin {
			return access
		}
	}

	access.CanEnter = true
	access.CanEditFurniture = sets.IsAdmin
	access.CanPublish = sets.IsAdmin
	access.CanReset = sets.IsAdmin
	return access
}

// loadRoomConfig returns the override row for a room, if any.
func (s *Service) loadRoomConfig(orgID string, ref models.OfficeRoomRef) (*models.OfficeRoomConfig, error) {
	var config models.OfficeRoomConfig
	err := s.Db.Postgresql.Where("organisation_id = ? AND room_kind = ? AND ref_id = ?", orgID, ref.Kind, ref.Ref).
		First(&config).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}
