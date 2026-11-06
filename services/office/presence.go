package office

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/centrifuge"
)

// Office realtime channels (PRD section 6.4).
//
//	office:org:{orgId}:floor:{floorIndex}:room:{encodedRoomId}:presence
//	office:org:{orgId}:floor:{floorIndex}:room:{encodedRoomId}:layout
//
// The organisation is part of the channel name on purpose: rooms such as the
// lobby or focus pod 1 exist in every organisation, so a name without the
// organisation would carry one organisation's presence to another's.
const (
	OfficeChannelPrefix   = "office_org_"
	PresenceChannelSuffix = "_presence"
	LayoutChannelSuffix   = "_layout"
)

// OfficeChannelTarget names the room a realtime message belongs to.
type OfficeChannelTarget struct {
	OrgID      string
	FloorIndex int
	RoomID     string
}

// OfficeChannel is a parsed office channel name.
type OfficeChannel struct {
	OrgID      string
	FloorIndex int
	RoomID     string
	Stream     string
}

// NewChannelTarget builds the channel target of a room from its reference
// alone. Shared rooms have a fixed floor; a personal office does not, so use
// RoomTarget (or ChannelTargetForFloor) whenever the owner's floor is
// available or can be looked up.
func NewChannelTarget(orgID string, ref models.OfficeRoomRef) OfficeChannelTarget {
	return ChannelTargetForFloor(orgID, ref, RoomFloorIndex(ref))
}

// ChannelTargetForFloor builds the channel target of a room listed on a known
// floor.
func ChannelTargetForFloor(orgID string, ref models.OfficeRoomRef, floor int) OfficeChannelTarget {
	return OfficeChannelTarget{
		OrgID:      orgID,
		FloorIndex: floor,
		RoomID:     EncodeRoomID(ref.Kind, ref.Ref),
	}
}

// RoomTarget builds the channel target of a room the way the directory lists
// it. A personal office follows its owner's tower floor, which is not part of
// the reference, so this path reads the stored assignment.
func (s *Service) RoomTarget(orgID string, ref models.OfficeRoomRef) (OfficeChannelTarget, error) {
	floor, err := s.RoomFloor(orgID, ref)
	if err != nil {
		return OfficeChannelTarget{}, err
	}

	return ChannelTargetForFloor(orgID, ref, floor), nil
}

// ChannelTarget resolves the channel a room's announcements belong to. The
// floor lookup must never drop an announcement: Redis and the database are the
// source of truth, publishing is best effort, so a failed lookup falls back to
// the reference-derived channel instead of returning an error.
func (s *Service) ChannelTarget(orgID string, ref models.OfficeRoomRef) OfficeChannelTarget {
	target, err := s.RoomTarget(orgID, ref)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("failed to resolve the floor of room %s: %v", ref.Ref, err)
		}
		return NewChannelTarget(orgID, ref)
	}

	return target
}

// RoomFloor returns the floor a room is listed on. Shared rooms have a fixed
// floor; a personal office sits on its owner's member floor, falling back to
// the first member floor when the owner has no assignment yet.
func (s *Service) RoomFloor(orgID string, ref models.OfficeRoomRef) (int, error) {
	if ref.Kind != models.OfficeRoomKindPersonal {
		return RoomFloorIndex(ref), nil
	}

	var row struct {
		FloorIndex *int
	}
	if err := s.Db.Postgresql.Model(&models.OrgUserManagement{}).
		Select("floor_index").
		Where("organisation_id = ? AND user_id = ?", orgID, ref.Ref).
		Scan(&row).Error; err != nil {
		return 0, err
	}

	if row.FloorIndex != nil && *row.FloorIndex >= models.OfficeFloorMember {
		return *row.FloorIndex, nil
	}

	return models.OfficeFloorMember, nil
}

// PresenceChannelName returns the Centrifugo channel carrying the presence of
// a room.
func PresenceChannelName(target OfficeChannelTarget) string {
	return officeChannelName(target, PresenceChannelSuffix)
}

// LayoutChannelName returns the Centrifugo channel announcing layout changes.
func LayoutChannelName(target OfficeChannelTarget) string {
	return officeChannelName(target, LayoutChannelSuffix)
}

func officeChannelName(target OfficeChannelTarget, suffix string) string {
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(target.RoomID))
	return fmt.Sprintf(
		"%s%s_floor_%d_room_%s%s",
		OfficeChannelPrefix,
		target.OrgID,
		target.FloorIndex,
		encoded,
		suffix,
	)
}

// IsOfficeChannel reports whether a channel name belongs to the office.
func IsOfficeChannel(channel string) bool {
	return strings.HasPrefix(channel, OfficeChannelPrefix)
}

// ParseOfficeChannel splits an office channel back into the organisation, the
// floor, the room and the stream it carries.
func ParseOfficeChannel(channel string) (OfficeChannel, bool) {
	if !IsOfficeChannel(channel) {
		return OfficeChannel{}, false
	}

	trimmed := strings.TrimPrefix(channel, OfficeChannelPrefix)
	stream := ""
	switch {
	case strings.HasSuffix(trimmed, PresenceChannelSuffix):
		trimmed = strings.TrimSuffix(trimmed, PresenceChannelSuffix)
		stream = "presence"
	case strings.HasSuffix(trimmed, LayoutChannelSuffix):
		trimmed = strings.TrimSuffix(trimmed, LayoutChannelSuffix)
		stream = "layout"
	default:
		return OfficeChannel{}, false
	}

	// trimmed is now: {orgId}_floor_{n}_room_{base64url(roomId)}
	floorMarker := "_floor_"
	roomMarker := "_room_"

	floorIdx := strings.Index(trimmed, floorMarker)
	if floorIdx < 0 {
		return OfficeChannel{}, false
	}
	orgID := trimmed[:floorIdx]
	if orgID == "" {
		return OfficeChannel{}, false
	}

	rest := trimmed[floorIdx+len(floorMarker):]
	roomIdx := strings.Index(rest, roomMarker)
	if roomIdx < 0 {
		return OfficeChannel{}, false
	}
	floorStr := rest[:roomIdx]

	floorIndex, err := strconv.Atoi(floorStr)
	if err != nil || floorIndex < 0 {
		return OfficeChannel{}, false
	}

	roomB64 := rest[roomIdx+len(roomMarker):]
	roomIDBytes, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(roomB64)
	if err != nil {
		return OfficeChannel{}, false
	}

	return OfficeChannel{
		OrgID:      orgID,
		FloorIndex: floorIndex,
		RoomID:     string(roomIDBytes),
		Stream:     stream,
	}, true
}

// PublishPresence announces that somebody entered, left or moved inside a room.
func (s *Service) PublishPresence(target OfficeChannelTarget, presence models.OfficeOccupantResponse, eventType string) error {
	payload := map[string]any{
		"type":     eventType,
		"presence": presence,
	}

	return centrifuge.PublishChannelOptional(s.Logger, PresenceChannelName(target), payload)
}

// PublishLayoutUpdate announces that the live layout of a room changed.
func (s *Service) PublishLayoutUpdate(target OfficeChannelTarget, version int64, updatedBy string) error {
	payload := map[string]any{
		"type":      "published",
		"version":   version,
		"roomId":    target.RoomID,
		"updatedBy": updatedBy,
	}

	return centrifuge.PublishChannelOptional(s.Logger, LayoutChannelName(target), payload)
}
