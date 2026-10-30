package test_office

import (
	"database/sql/driver"
	"encoding/base64"
	"strconv"
	"testing"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
)

// presenceChannel is the channel name the frontend subscribes to, built here
// from the published format rather than from the implementation.
func presenceChannel(orgID string, ref models.OfficeRoomRef, floor int) string {
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(office.EncodeRoomID(ref.Kind, ref.Ref)))

	return office.OfficeChannelPrefix + orgID + "_floor_" + strconv.Itoa(floor) + "_room_" + encoded + office.PresenceChannelSuffix
}

// A personal office is listed on its owner's tower floor, so its presence has
// to be announced there. Publishing on floor 1 leaves every client that
// subscribed through the bootstrap directory deaf to the room.
func TestPersonalOfficePresenceUsesTheOwnerFloor(t *testing.T) {
	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindPersonal, Ref: "usr-2"}
	want := presenceChannel("org-1", ref, 2)

	// The fake database answers one query per connection, so every lookup
	// gets its own service.
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, service *office.Service)
	}{
		{"floor", func(t *testing.T, service *office.Service) {
			floor, err := service.RoomFloor("org-1", ref)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if floor != 2 {
				t.Fatalf("floor: got %d, want 2", floor)
			}
		}},
		{"target", func(t *testing.T, service *office.Service) {
			target, err := service.RoomTarget("org-1", ref)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target.FloorIndex != 2 {
				t.Fatalf("target floor: got %d, want 2", target.FloorIndex)
			}
			if got := office.PresenceChannelName(target); got != want {
				t.Fatalf("presence channel: got %q, want %q", got, want)
			}
		}},
		{"announcement", func(t *testing.T, service *office.Service) {
			if got := office.PresenceChannelName(service.ChannelTarget("org-1", ref)); got != want {
				t.Fatalf("announcement channel: got %q, want %q", got, want)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := officeTestDB(t, []string{"floor_index"}, [][]driver.Value{{int64(2)}})
			tc.run(t, officeService(t, db))
		})
	}
}

// A member who has not been placed in the tower yet keeps the first member
// floor, which is where the directory lists them too.
func TestPersonalOfficePresenceFallsBackToTheFirstMemberFloor(t *testing.T) {
	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindPersonal, Ref: "usr-3"}
	db := officeTestDB(t, []string{"floor_index"}, [][]driver.Value{{nil}})
	service := officeService(t, db)

	target, err := service.RoomTarget("org-1", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.FloorIndex != models.OfficeFloorMember {
		t.Fatalf("target floor: got %d, want %d", target.FloorIndex, models.OfficeFloorMember)
	}
}

// Shared rooms keep the floor their reference encodes; the owner lookup only
// applies to personal offices.
func TestSharedRoomPresenceKeepsItsFloor(t *testing.T) {
	service := officeService(t, officeTestDB(t, []string{"floor_index"}, nil))

	for _, tc := range []struct {
		name string
		ref  models.OfficeRoomRef
		want int
	}{
		{"channel", models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: "ch-1"}, models.OfficeFloorGeneral},
		{"lobby", models.OfficeRoomRef{Kind: models.OfficeRoomKindLobby, Ref: "lobby"}, models.OfficeFloorGeneral},
		{"meeting", models.OfficeRoomRef{Kind: models.OfficeRoomKindMeeting, Ref: "1"}, models.OfficeFloorGeneral},
		{"area", models.OfficeRoomRef{Kind: models.OfficeRoomKindArea, Ref: "3:pods"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			floor, err := service.RoomFloor("org-1", tc.ref)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if floor != tc.want {
				t.Fatalf("floor: got %d, want %d", floor, tc.want)
			}
		})
	}
}

// The reference-only constructor answers the same shared-room channels it
// always did, so callers that do not need the owner lookup are unaffected.
func TestNewChannelTargetKeepsSharedRoomChannels(t *testing.T) {
	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: "ch-1"}

	got := office.PresenceChannelName(office.NewChannelTarget("org-1", ref))
	want := presenceChannel("org-1", ref, models.OfficeFloorGeneral)
	if got != want {
		t.Fatalf("channel: got %q, want %q", got, want)
	}
}
