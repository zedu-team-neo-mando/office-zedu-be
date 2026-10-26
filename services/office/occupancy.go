package office

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
)

const (
	// presenceTTL keeps stale positions from lingering when a client goes away
	// without sending a leave event.
	presenceTTL = 5 * time.Minute
	// presenceKeyPrefix namespaces the presence hashes in Redis.
	presenceKeyPrefix = "office:presence:"
)

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

	return occupants, nil
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
