package office

import (
	"fmt"
	"strings"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
	"gorm.io/gorm"
)

// The building grid. Room bounds are expressed in these units; the tile size
// matches the client grid (1 unit = 1 tile).
const (
	OfficeBuildingWidth  = 56.0
	OfficeBuildingHeight = 36.0
	OfficeTileSize       = 1.0
)

// DefaultMeetingRooms and DefaultFocusPods are the shared rooms every
// organisation gets before any office_room_configs override is applied.
const (
	DefaultMeetingRooms = 2
	DefaultFocusPods    = 4
)

// Directory is the derived room list for one requester.
type Directory struct {
	Rooms          []models.OfficeRoomResponse
	Members        []models.OfficeMemberResponse
	ChannelRoomMap map[string]string
	DMOfficeMap    map[string]string
	Floors         map[string]int
}

// defaultBounds returns the derived footprint for a room. Channel and personal
// rooms are laid out deterministically so every client agrees on the map.
func defaultBounds(kind, ref string, index int) models.OfficeBounds {
	switch kind {
	case models.OfficeRoomKindLobby:
		return models.OfficeBounds{X: 1, Y: 1, W: 8, H: 6}
	case models.OfficeRoomKindReception:
		return models.OfficeBounds{X: 9, Y: 1, W: 8, H: 6}
	case models.OfficeRoomKindLounge:
		// One corridor below the lobby/reception pair, so both doors open into
		// walkable floor instead of sharing an edge.
		return models.OfficeBounds{X: 1, Y: 8.6, W: 8, H: 6}
	case models.OfficeRoomKindMeeting:
		return models.OfficeBounds{X: 18 + float64(index)*7, Y: 1, W: 6, H: 6}
	case models.OfficeRoomKindFocus:
		return models.OfficeBounds{X: 18 + float64(index%2)*5, Y: 8 + float64(index/2)*5, W: 4, H: 4}
	case models.OfficeRoomKindChannel:
		// Channel rooms sit in rows below the shared rooms: seven 7.3-wide
		// rooms fit the 56-wide building, and the 1.6 corridor between rows
		// keeps every doorway reachable. Two rows (14 channels) fit inside a
		// 36-deep footprint.
		row := index / 7
		col := index % 7
		return models.OfficeBounds{
			X: 0.8 + float64(col)*7.9,
			Y: 19 + float64(row)*9,
			W: 7.3,
			H: 7.4,
		}
	case models.OfficeRoomKindPersonal:
		// Ten full offices per row, four rows per member floor. A 5 by 5.4
		// room is an office somebody can furnish, not a cubicle; the 1.6
		// corridor between rows is where every door opens.
		slot := index % models.OfficeFloorCapacity
		tier := slot / 10
		column := slot % 10
		return models.OfficeBounds{
			X: 1.2 + float64(column)*5.4,
			Y: 1 + float64(tier)*7,
			W: 5,
			H: 5.4,
		}
	case models.OfficeRoomKindArea:
		// The three extras share a strip below the offices, so a member never
		// has to walk to another storey to find them.
		return models.OfficeBounds{X: 2 + float64(index)*18, Y: 28.6, W: 16, H: 6.4}
	default:
		return models.OfficeBounds{X: 0, Y: 0, W: 4, H: 4}
	}
}

func defaultCapacity(kind string) int {
	switch kind {
	case models.OfficeRoomKindLobby, models.OfficeRoomKindReception:
		return 60
	case models.OfficeRoomKindLounge:
		return 30
	case models.OfficeRoomKindChannel:
		return 30
	case models.OfficeRoomKindMeeting:
		return 8
	case models.OfficeRoomKindFocus:
		return 1
	case models.OfficeRoomKindPersonal:
		return 4
	case models.OfficeRoomKindArea:
		return 12
	default:
		return 8
	}
}

// BuildDirectory derives the rooms a requester can see.
//
// Shared rooms always exist, every channel in the organisation is a door on the
// general floor and every member has an office in the private wing. A room the
// requester may not enter is still listed, marked as locked, so the building
// looks the same to everybody and only the doors differ.
func (s *Service) BuildDirectory(orgID, userID string, sets AccessSets) (*Directory, error) {
	directory := &Directory{
		Rooms:          []models.OfficeRoomResponse{},
		Members:        []models.OfficeMemberResponse{},
		ChannelRoomMap: map[string]string{},
		DMOfficeMap:    map[string]string{},
	}

	overrides, err := s.loadRoomConfigs(orgID)
	if err != nil {
		return nil, err
	}

	floors, err := s.SyncFloorAssignments(orgID)
	if err != nil {
		return nil, err
	}
	directory.Floors = floors

	appendRoom := func(kind, ref, name string, index int, locked bool) {
		bounds := defaultBounds(kind, ref, index)
		capacity := defaultCapacity(kind)
		enabled := true

		if override, ok := overrides[roomKey(kind, ref)]; ok {
			if override.Bounds.W > 0 && override.Bounds.H > 0 {
				bounds = override.Bounds
			}
			if override.Capacity > 0 {
				capacity = override.Capacity
			}
			enabled = override.Enabled
		}

		if !enabled {
			return
		}

		directory.Rooms = append(directory.Rooms, models.OfficeRoomResponse{
			ID:         EncodeRoomID(kind, ref),
			Kind:       kind,
			Name:       name,
			RefID:      ref,
			Capacity:   capacity,
			IsLocked:   locked,
			FloorIndex: RoomFloorIndex(models.OfficeRoomRef{Kind: kind, Ref: ref}),
			Bounds:     bounds,
		})
	}

	// Shared rooms.
	appendRoom(models.OfficeRoomKindLobby, "", "Lobby", 0, false)
	appendRoom(models.OfficeRoomKindReception, "", "Reception", 0, false)
	appendRoom(models.OfficeRoomKindLounge, "", "Lounge", 0, false)
	for index := 0; index < DefaultMeetingRooms; index++ {
		appendRoom(models.OfficeRoomKindMeeting, fmt.Sprintf("%d", index+1), fmt.Sprintf("Meeting %d", index+1), index, false)
	}
	for index := 0; index < DefaultFocusPods; index++ {
		appendRoom(models.OfficeRoomKindFocus, fmt.Sprintf("%d", index+1), fmt.Sprintf("Focus %02d", index+1), index, false)
	}

	// Channel rooms: the whole organisation's list, so the general floor is
	// one plan for everyone.
	channels, err := s.channelsForUser(orgID, userID)
	if err != nil {
		return nil, err
	}
	for index, channel := range channels {
		roomID := EncodeRoomID(models.OfficeRoomKindChannel, channel.ID)
		directory.ChannelRoomMap[channel.ID] = roomID
		// A private channel the requester is not in is still a door on the
		// general floor; it is just locked until they join it in Zedu.
		locked := channel.IsPrivate && !channel.Joined && !sets.IsAdmin
		appendRoom(models.OfficeRoomKindChannel, channel.ID, channel.Name, index, locked)
	}

	// Personal offices follow the organisation's members. The tower is a
	// directory of the whole team, so an office the requester may not enter is
	// still listed by name and marked as locked; the door is what is closed,
	// not the person. The slot is taken from the full member list so the wing
	// looks the same for everybody.
	members, err := s.organisationMembers(orgID)
	if err != nil {
		return nil, err
	}

	for index, member := range members {
		ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindPersonal, Ref: member.UserID}
		access := CanAccessFromSets(sets, ref, userID)
		floor := memberFloor(floors, member.UserID)
		// The slot comes from the full member list so the wing looks the same
		// for everybody, but the floor comes from the tower assignment.
		before := len(directory.Rooms)
		appendRoom(models.OfficeRoomKindPersonal, member.UserID, member.DisplayName, index, !access.CanEnter)
		if len(directory.Rooms) > before {
			directory.Rooms[len(directory.Rooms)-1].FloorIndex = floor
		}

		// The tower needs every member by identity, not only the people whose
		// office this requester can walk into. This is the same roster the
		// 2D member list uses, with the office room it belongs to.
		directory.Members = append(directory.Members, models.OfficeMemberResponse{
			UserID:      member.UserID,
			DisplayName: member.DisplayName,
			Username:    member.Username,
			Title:       member.Title,
			AvatarURL:   member.AvatarURL,
			Tint:        member.Tint,
			Role:        member.Role,
			FloorIndex:  floor,
			RoomID:      EncodeRoomID(models.OfficeRoomKindPersonal, member.UserID),
			Online:      member.Online,
			Status:      member.Status,
			StatusText:  member.StatusText,
			CanVisit:    access.CanEnter,
		})
	}

	// Every member floor carries its extras. They are rooms in their own right
	// so presence and access follow them, and nobody is allocated a desk there.
	for floor := models.OfficeFloorMember; floor <= highestFloor(floors); floor++ {
		for index, area := range AreaRoomsForFloor(floor) {
			_, key, ok := ParseAreaRef(area.Ref)
			if !ok {
				continue
			}
			appendRoom(area.Kind, area.Ref, AreaRoomName(key), index, false)
		}
	}

	// Direct messages map to the counterpart's office.
	dmMap, err := s.dmOfficeMap(orgID, userID)
	if err != nil {
		return nil, err
	}
	directory.DMOfficeMap = dmMap

	return directory, nil
}

// GetRoom returns a single room by its reference, using the precomputed access sets.
// It builds only the necessary room without constructing the entire directory.
func (s *Service) GetRoom(orgID, userID string, sets AccessSets, ref models.OfficeRoomRef) (*models.OfficeRoomResponse, error) {
	overrides, err := s.loadRoomConfigs(orgID)
	if err != nil {
		return nil, err
	}

	// Check access first
	access := CanAccessFromSets(sets, ref, userID)
	if !access.CanEnter {
		return nil, nil // caller will handle 403/404
	}

	floors, err := s.SyncFloorAssignments(orgID)
	if err != nil {
		return nil, err
	}

	// Build just this room
	kind := ref.Kind
	refStr := ref.Ref
	name := ""
	index := 0
	locked := false

	switch kind {
	case models.OfficeRoomKindLobby:
		name = "Lobby"
	case models.OfficeRoomKindReception:
		name = "Reception"
	case models.OfficeRoomKindLounge:
		name = "Lounge"
	case models.OfficeRoomKindMeeting:
		name = fmt.Sprintf("Meeting %s", refStr)
	case models.OfficeRoomKindFocus:
		name = fmt.Sprintf("Focus %02s", refStr)
	case models.OfficeRoomKindChannel:
		// Need to get channel name
		channels, err := s.channelsForUser(orgID, "")
		if err == nil {
			for _, ch := range channels {
				if ch.ID == refStr {
					name = ch.Name
					break
				}
			}
		}
		if name == "" {
			name = fmt.Sprintf("Channel %s", refStr)
		}
	case models.OfficeRoomKindPersonal:
		// Get member display name
		members, err := s.organisationMembers(orgID)
		if err == nil {
			for _, m := range members {
				if m.UserID == refStr {
					name = m.DisplayName
					break
				}
			}
		}
		if name == "" {
			name = fmt.Sprintf("Office %s", refStr)
		}
	case models.OfficeRoomKindArea:
		if _, key, ok := ParseAreaRef(refStr); ok {
			name = AreaRoomName(key)
		} else {
			name = "Area"
		}
	}

	bounds := defaultBounds(kind, refStr, index)
	capacity := defaultCapacity(kind)
	enabled := true

	if override, ok := overrides[roomKey(kind, refStr)]; ok {
		if override.Bounds.W > 0 && override.Bounds.H > 0 {
			bounds = override.Bounds
		}
		if override.Capacity > 0 {
			capacity = override.Capacity
		}
		enabled = override.Enabled
	}

	if !enabled {
		return nil, nil
	}

	floorIndex := RoomFloorIndex(ref)
	if kind == models.OfficeRoomKindPersonal {
		floorIndex = memberFloor(floors, refStr)
	}

	return &models.OfficeRoomResponse{
		ID:         EncodeRoomID(kind, refStr),
		Kind:       kind,
		Name:       name,
		RefID:      refStr,
		Capacity:   capacity,
		IsLocked:   locked,
		FloorIndex: floorIndex,
		Bounds:     bounds,
	}, nil
}

func roomKey(kind, ref string) string {
	return kind + ":" + ref
}

// loadRoomConfigs returns the room overrides of an organisation keyed by
// "kind:ref".
func (s *Service) loadRoomConfigs(orgID string) (map[string]models.OfficeRoomConfig, error) {
	var configs []models.OfficeRoomConfig
	if err := s.Db.Postgresql.Where("organisation_id = ?", orgID).Find(&configs).Error; err != nil {
		return nil, err
	}

	overrides := make(map[string]models.OfficeRoomConfig, len(configs))
	for _, config := range configs {
		overrides[roomKey(config.RoomKind, config.RefID)] = config
	}
	return overrides, nil
}

// channelSummary is the little bit of channel data the directory needs.
type channelSummary struct {
	ID        string
	Name      string
	IsPrivate bool
	Joined    bool
}

// Active channels may have NULL or Go's zero timestamp in the legacy
// non-nullable Channels.DeletedAt column. A real deletion timestamp stays excluded.
func activeOfficeChannels(db *gorm.DB) *gorm.DB {
	return db.Where("channels.deleted_at IS NULL OR channels.deleted_at = ?", time.Time{})
}

// channelsForUser returns every channel in the organisation, with the
// requester's membership flag. Public channels are open to the whole org (the
// same access the 2D chat gives); private channels are doors the requester may
// see but not walk through until they join.
func (s *Service) channelsForUser(orgID, userID string) ([]channelSummary, error) {
	type row struct {
		ID        string
		Name      string
		IsPrivate bool
		Joined    bool
	}

	var rows []row
	err := s.Db.Postgresql.Table("channels").
		Select("channels.id, channels.name, COALESCE(channels.is_private, false) AS is_private, (user_channels.user_id IS NOT NULL) AS joined").
		Joins("LEFT JOIN user_channels ON user_channels.channels_id = channels.id AND user_channels.user_id = ?", userID).
		Where("channels.organisation_id = ?", orgID).
		Scopes(activeOfficeChannels).
		Order("channels.created_at ASC, channels.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	channels := make([]channelSummary, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, channelSummary{
			ID:        row.ID,
			Name:      row.Name,
			IsPrivate: row.IsPrivate,
			Joined:    row.Joined,
		})
	}
	return channels, nil
}

// memberSummary is a member of the organisation as the tower needs them.
type memberSummary struct {
	UserID      string
	DisplayName string
	Username    string
	Title       string
	AvatarURL   string
	Tint        string
	Role        string
	Online      bool
	Status      string
	StatusText  string
}

// RoomCapacity is the number of people a room holds. A focus pod holds one
// person, a meeting room a handful, a channel room a crowd.
func (s *Service) RoomCapacity(orgID string, ref models.OfficeRoomRef) int {
	capacity := defaultCapacity(ref.Kind)

	var config models.OfficeRoomConfig
	err := s.Db.Postgresql.Where("organisation_id = ? AND room_kind = ? AND ref_id = ?", orgID, ref.Kind, ref.Ref).
		First(&config).Error
	if err == gorm.ErrRecordNotFound {
		return capacity
	}
	if err != nil {
		return capacity
	}
	if config.Capacity > 0 {
		return config.Capacity
	}

	return capacity
}

// organisationMembers returns the active members of the organisation with the
// identity the tower shows.
func (s *Service) organisationMembers(orgID string) ([]memberSummary, error) {
	type row struct {
		UserID      string
		DisplayName string
		Username    string
		Title       string
		AvatarURL   string
		Tint        string
		Role        string
		Online      bool
		StatusText  string
	}

	var rows []row
	// A member can have an organisation profile and a global one; picking one
	// with LATERAL keeps a person from appearing twice in the tower. The name
	// falls back through every name the account has, so a member who never set
	// a display name is still called something recognisable.
	err := s.Db.Postgresql.Table("org_user_managements AS m").
		Select(`m.user_id,
			COALESCE(NULLIF(p.name, ''), NULLIF(u.name, ''), NULLIF(SPLIT_PART(u.email, '@', 1), ''), 'Teammate') AS display_name,
			COALESCE(p.username, '') AS username,
			COALESCE(p.title, '') AS title,
			COALESCE(p.avatar_url, '') AS avatar_url,
			COALESCE(p.tint, '') AS tint,
			COALESCE(r.name, '') AS role,
			COALESCE(p.online, false) AS online,
			COALESCE(p.status_text, '') AS status_text`).
		Joins(`LEFT JOIN LATERAL (
			SELECT COALESCE(NULLIF(display_name, ''), NULLIF(user_name, ''), NULLIF(full_name, '')) AS name,
				COALESCE(user_name, '') AS username,
				COALESCE(title, '') AS title,
				COALESCE(avatar_url, '') AS avatar_url,
				COALESCE(BTRIM(avatar_config->>'tint'), '') AS tint,
				COALESCE(online, false) AS online,
				COALESCE(text, '') AS status_text
			FROM profiles
			WHERE userid = m.user_id AND deleted_at IS NULL
				AND (organisation_id = m.organisation_id OR organisation_id IS NULL)
			ORDER BY (organisation_id IS NULL), created_at DESC
			LIMIT 1
		) p ON TRUE`).
		Joins("LEFT JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL").
		Joins("LEFT JOIN org_roles r ON r.id = m.role_id AND r.deleted_at IS NULL").
		Where("m.organisation_id = ?", orgID).
		Where("m.is_deactivated = ?", false).
		Order("m.created_at ASC, m.user_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	members := make([]memberSummary, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.DisplayName)
		if name == "" {
			name = "Teammate"
		}
		status := "away"
		if row.Online {
			status = "online"
		}
		members = append(members, memberSummary{
			UserID:      row.UserID,
			DisplayName: name,
			Username:    row.Username,
			Title:       row.Title,
			AvatarURL:   row.AvatarURL,
			Tint:        row.Tint,
			Role:        row.Role,
			Online:      row.Online,
			Status:      status,
			StatusText:  row.StatusText,
		})
	}
	return members, nil
}

// DisplayName is the name a member is known by inside the office.
func (s *Service) DisplayName(orgID, userID string) string {
	var profile models.Profile
	err := s.Db.Postgresql.Where("userid = ? AND (organisation_id IS NULL OR organisation_id = ?)", userID, orgID).
		First(&profile).Error
	if err != nil {
		return ""
	}

	if profile.DisplayName != "" {
		return profile.DisplayName
	}

	var user models.User
	if err := s.Db.Postgresql.Where("id = ?", userID).First(&user).Error; err == nil {
		return user.Name
	}

	return ""
}

// dmOfficeMap maps the id of a 1:1 direct message to the personal office of
// the other participant. It is built per requester, so the entries only ever
// point at rooms the requester may visit.
//
// Group DMs are deliberately absent: a group has no single other participant,
// so it has no office to bind to. Binding the requester's own office to the
// last group DM made opening their own office start a group conversation.
func (s *Service) dmOfficeMap(orgID, userID string) (map[string]string, error) {
	officeMap := map[string]string{}

	// 1:1 direct messages.
	//
	// Only the two columns this map needs are selected. Reading every column
	// of dm_channels into plain strings breaks as soon as a nullable field
	// such as channel_type holds NULL, which fails the scan and takes the
	// whole office bootstrap down with a 500.
	type dmRow struct {
		ChannelID     string
		ParticipantID *string
	}

	var rows []dmRow
	err := s.Db.Postgresql.Model(&models.DmChannels{}).
		Select("channel_id, participant_id").
		Where("org_id = ? AND user_id = ?", orgID, userID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		if row.ParticipantID == nil || *row.ParticipantID == "" {
			continue
		}
		officeMap[row.ChannelID] = EncodeRoomID(models.OfficeRoomKindPersonal, *row.ParticipantID)
	}

	return officeMap, nil
}
