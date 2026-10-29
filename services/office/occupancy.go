package office

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/hngprojects/telex_be/internal/models"
)

const (
	// presenceTTL keeps stale positions from lingering when a client goes away
	// without sending a leave event.
	presenceTTL = 5 * time.Minute
	// presenceKeyPrefix namespaces the presence hashes in Redis.
	presenceKeyPrefix = "office:presence:"
	// presenceUpdateKeyPrefix namespaces the per-person update slots used to
	// throttle movement.
	presenceUpdateKeyPrefix = "office:presence_update:"
	// presenceUpdateInterval is the minimum time between two accepted updates
	// from the same person in the same room.
	presenceUpdateInterval = 100 * time.Millisecond
)

var (
	// ErrNotInRoom means the requester has no live presence entry in the room;
	// an update moves an occupant and never creates one.
	ErrNotInRoom = errors.New("the requester is not in this room")
	// ErrPositionOutOfBounds means the position lies outside the building
	// footprint of 56 by 36.
	ErrPositionOutOfBounds = errors.New("the position is outside the building")
	// ErrTooManyUpdates means the requester moved again before the previous
	// accepted update had aged.
	ErrTooManyUpdates = errors.New("too many presence updates")
)

// RoomCentrePosition is the spot a new occupant appears at: the middle of the
// room footprint. The room grid maps one to one onto the world, with the room
// y axis becoming the world z axis, and the floor is always world y zero.
func RoomCentrePosition(bounds models.OfficeBounds) models.OfficeVector {
	return models.OfficeVector{
		X: bounds.X + bounds.W/2,
		Y: 0,
		Z: bounds.Y + bounds.H/2,
	}
}

// PositionWithinBuilding reports whether a world position lies inside the
// building footprint, boundaries included.
func PositionWithinBuilding(position models.OfficeVector) bool {
	return position.X >= 0 && position.X <= OfficeBuildingWidth &&
		position.Z >= 0 && position.Z <= OfficeBuildingHeight
}

// LiveOccupants drops the presence entries that outlived presenceTTL. The
// hash key expires as a whole, so in a room where somebody keeps moving a
// departed occupant would otherwise linger forever.
func LiveOccupants(occupants []models.OfficeOccupantResponse, now time.Time) []models.OfficeOccupantResponse {
	live := make([]models.OfficeOccupantResponse, 0, len(occupants))
	for _, occupant := range occupants {
		if now.Sub(occupant.UpdatedAt) <= presenceTTL {
			live = append(live, occupant)
		}
	}
	return live
}

// presenceKey names the hash holding the people in a room. The organisation
// is part of the key: rooms such as the lobby exist in every organisation, so
// a key without it would mix their occupants together.
func presenceKey(orgID, roomID string) string {
	return fmt.Sprintf("%s%s:%s", presenceKeyPrefix, orgID, roomID)
}

// JoinRoom records that a user is standing in a room and announces it.
func (s *Service) JoinRoom(command models.OfficePresenceCommand) error {
	roomID := EncodeRoomID(command.Room.Kind, command.Room.Ref)
	presence := command.Presence
	presence.RoomID = roomID
	presence.UpdatedAt = time.Now().UTC()

	if err := s.storePresence(command.OrganisationID, roomID, command.UserID, presence); err != nil {
		return err
	}

	target := NewChannelTarget(command.OrganisationID, command.Room)
	if err := s.PublishPresence(target, presence, "join"); err != nil {
		// Publish is an announcement; Redis is the source of truth. A
		// Centrifugo hiccup must not 500 a room entry.
		s.Logger.Error("failed to publish join to %s: %v", PresenceChannelName(target), err)
	}
	return nil
}

// LeaveRoom removes a user from a room and announces it.
func (s *Service) LeaveRoom(orgID, userID string, ref models.OfficeRoomRef) error {
	roomID := EncodeRoomID(ref.Kind, ref.Ref)

	if s.Db.Redis != nil {
		ctx := context.Background()
		key := presenceKey(orgID, roomID)
		if err := s.Db.Redis.HDel(ctx, key, userID).Err(); err != nil {
			return err
		}
	}

	presence := models.OfficeOccupantResponse{
		UserID: userID,
		RoomID: roomID,
	}
	target := NewChannelTarget(orgID, ref)
	if err := s.PublishPresence(target, presence, "leave"); err != nil {
		// Publish is an announcement; Redis is the source of truth. A
		// Centrifugo hiccup must not 500 a room leave.
		s.Logger.Error("failed to publish leave to %s: %v", PresenceChannelName(target), err)
	}
	return nil
}

// UpdatePresence moves a live occupant inside a room and announces it. It
// never creates an entry: entering the room is what puts somebody in it.
func (s *Service) UpdatePresence(command models.OfficePresenceUpdateCommand) error {
	roomID := EncodeRoomID(command.Room.Kind, command.Room.Ref)

	current, found, err := s.liveOccupant(command.OrganisationID, roomID, command.UserID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotInRoom
	}

	if !PositionWithinBuilding(command.Position) {
		return ErrPositionOutOfBounds
	}

	// The throttle is the last check before accepting: a rejected update must
	// not spend the slot of an accepted one.
	if err := s.throttleUpdate(command.OrganisationID, roomID, command.UserID); err != nil {
		return err
	}

	if command.Status != "" {
		current.Status = command.Status
	}
	current.Position = command.Position
	current.Rotation = command.Rotation
	current.UpdatedAt = time.Now().UTC()

	if err := s.storePresence(command.OrganisationID, roomID, command.UserID, current); err != nil {
		return err
	}

	target := NewChannelTarget(command.OrganisationID, command.Room)
	if err := s.PublishPresence(target, current, "update"); err != nil {
		// Publish is an announcement; Redis is the source of truth. A
		// Centrifugo hiccup must not fail a movement update.
		s.Logger.Error("failed to publish update to %s: %v", PresenceChannelName(target), err)
	}
	return nil
}

// liveOccupant reads one person's presence entry from the room's hash.
func (s *Service) liveOccupant(orgID, roomID, userID string) (models.OfficeOccupantResponse, bool, error) {
	if s.Db.Redis == nil {
		return models.OfficeOccupantResponse{}, false, nil
	}

	raw, err := s.Db.Redis.HGet(context.Background(), presenceKey(orgID, roomID), userID).Result()
	if errors.Is(err, redis.Nil) {
		return models.OfficeOccupantResponse{}, false, nil
	}
	if err != nil {
		return models.OfficeOccupantResponse{}, false, err
	}

	var occupant models.OfficeOccupantResponse
	if err := json.Unmarshal([]byte(raw), &occupant); err != nil {
		return models.OfficeOccupantResponse{}, false, err
	}
	return occupant, true, nil
}

// throttleUpdate allows at most one accepted update per person and room per
// interval. SET NX is a single atomic command, so two concurrent requests
// cannot both read the previous timestamp and both be accepted.
func (s *Service) throttleUpdate(orgID, roomID, userID string) error {
	if s.Db.Redis == nil {
		return nil
	}

	key := fmt.Sprintf("%s%s:%s:%s", presenceUpdateKeyPrefix, orgID, roomID, userID)
	accepted, err := s.Db.Redis.SetNX(context.Background(), key, time.Now().UnixNano(), presenceUpdateInterval).Result()
	if err != nil {
		return err
	}
	if !accepted {
		return ErrTooManyUpdates
	}
	return nil
}

// Occupants returns the people currently standing in a room.
func (s *Service) Occupants(orgID string, ref models.OfficeRoomRef) ([]models.OfficeOccupantResponse, error) {
	occupants := []models.OfficeOccupantResponse{}
	if s.Db.Redis == nil {
		return occupants, nil
	}

	roomID := EncodeRoomID(ref.Kind, ref.Ref)
	ctx := context.Background()
	values, err := s.Db.Redis.HGetAll(ctx, presenceKey(orgID, roomID)).Result()
	if err != nil {
		return nil, err
	}

	for _, raw := range values {
		var occupant models.OfficeOccupantResponse
		if err := json.Unmarshal([]byte(raw), &occupant); err != nil {
			continue
		}
		occupants = append(occupants, occupant)
	}

	return LiveOccupants(occupants, time.Now().UTC()), nil
}

// storePresence writes one person's presence into the room's hash.
func (s *Service) storePresence(orgID, roomID, userID string, presence models.OfficeOccupantResponse) error {
	if s.Db.Redis == nil {
		return nil
	}

	payload, err := json.Marshal(presence)
	if err != nil {
		return err
	}

	key := presenceKey(orgID, roomID)
	ctx := context.Background()
	if err := s.Db.Redis.HSet(ctx, key, userID, payload).Err(); err != nil {
		return err
	}
	return s.Db.Redis.Expire(ctx, key, presenceTTL).Err()
}
