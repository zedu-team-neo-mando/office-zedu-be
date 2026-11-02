package test_office

import (
	"testing"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
)

func TestParseRoomID(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantKind  string
		wantRef   string
		wantError bool
	}{
		{name: "lobby", input: "lobby", wantKind: models.OfficeRoomKindLobby},
		{name: "reception", input: "reception", wantKind: models.OfficeRoomKindReception},
		{name: "lounge", input: "lounge", wantKind: models.OfficeRoomKindLounge},
		{name: "channel", input: "channel:ch_1", wantKind: models.OfficeRoomKindChannel, wantRef: "ch_1"},
		{name: "personal", input: "personal:usr_2", wantKind: models.OfficeRoomKindPersonal, wantRef: "usr_2"},
		{name: "meeting", input: "meeting:3", wantKind: models.OfficeRoomKindMeeting, wantRef: "3"},
		{name: "focus", input: "focus:1", wantKind: models.OfficeRoomKindFocus, wantRef: "1"},
		{name: "portal", input: "portal:2", wantKind: models.OfficeRoomKindPortal, wantRef: "2"},
		{name: "portal with a padded floor", input: "portal:02", wantKind: models.OfficeRoomKindPortal, wantRef: "2"},
		{name: "portal with a signed floor", input: "portal:+1", wantKind: models.OfficeRoomKindPortal, wantRef: "1"},
		{name: "url encoded", input: "channel%3Ach_1", wantKind: models.OfficeRoomKindChannel, wantRef: "ch_1"},
		{name: "empty", input: "", wantError: true},
		{name: "unknown kind", input: "spaceship:1", wantError: true},
		{name: "channel without ref", input: "channel", wantError: true},
		{name: "channel with empty ref", input: "channel:", wantError: true},
		{name: "portal without ref", input: "portal", wantError: true},
		{name: "portal with a bad floor", input: "portal:basement", wantError: true},
		{name: "portal with a negative floor", input: "portal:-1", wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := office.ParseRoomID(tc.input)

			if tc.wantError {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if ref.Kind != tc.wantKind {
				t.Fatalf("kind: got %q, want %q", ref.Kind, tc.wantKind)
			}
			if ref.Ref != tc.wantRef {
				t.Fatalf("ref: got %q, want %q", ref.Ref, tc.wantRef)
			}
		})
	}
}

func TestEncodeRoomID(t *testing.T) {
	if got := office.EncodeRoomID(models.OfficeRoomKindChannel, "ch_1"); got != "channel:ch_1" {
		t.Fatalf("got %q, want %q", got, "channel:ch_1")
	}
	if got := office.EncodeRoomID(models.OfficeRoomKindLobby, ""); got != "lobby" {
		t.Fatalf("got %q, want %q", got, "lobby")
	}
}

func TestPortalRoomsCarryTheirStorey(t *testing.T) {
	member := office.PortalRoomRef(models.OfficeFloorMember)
	if member.Kind != models.OfficeRoomKindPortal || member.Ref != "1" {
		t.Fatalf("unexpected member portal reference: %+v", member)
	}
	if floor := office.RoomFloorIndex(member); floor != models.OfficeFloorMember {
		t.Fatalf("member portal floor: got %d, want %d", floor, models.OfficeFloorMember)
	}

	general := office.PortalRoomRef(models.OfficeFloorGeneral)
	if floor := office.RoomFloorIndex(general); floor != models.OfficeFloorGeneral {
		t.Fatalf("general portal floor: got %d, want %d", floor, models.OfficeFloorGeneral)
	}

	if name := office.PortalRoomName(models.OfficeFloorGeneral); name != "Private floors" {
		t.Fatalf("general portal name: got %q", name)
	}
	if name := office.PortalRoomName(models.OfficeFloorMember); name != "Channels" {
		t.Fatalf("member portal name: got %q", name)
	}
}

func TestHasPortalFloor(t *testing.T) {
	oneFloor := map[string]int{"usr_1": models.OfficeFloorMember}
	twoFloors := map[string]int{
		"usr_1": models.OfficeFloorMember,
		"usr_2": models.OfficeFloorMember + 1,
	}

	if !office.HasPortalFloor(oneFloor, models.OfficeFloorGeneral) {
		t.Fatal("the channels floor leads to the member floor")
	}
	if !office.HasPortalFloor(oneFloor, models.OfficeFloorMember) {
		t.Fatal("the member floor carries its own portal")
	}
	if office.HasPortalFloor(oneFloor, models.OfficeFloorMember+1) {
		t.Fatal("a floor above the highest assignment has no portal")
	}
	if !office.HasPortalFloor(twoFloors, models.OfficeFloorMember+1) {
		t.Fatal("the second member floor keeps its portal")
	}
	if office.HasPortalFloor(map[string]int{}, models.OfficeFloorGeneral) {
		t.Fatal("no member floor means no doorway to lead through")
	}
}

func TestRoomIDRoundTrip(t *testing.T) {
	for _, id := range []string{"channel:ch_1", "personal:usr_2", "meeting:1", "focus:4"} {
		ref, err := office.ParseRoomID(id)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", id, err)
		}
		if got := office.EncodeRoomID(ref.Kind, ref.Ref); got != id {
			t.Fatalf("round trip: got %q, want %q", got, id)
		}
	}
}

func TestIsSharedRoom(t *testing.T) {
	if office.IsSharedRoom(models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: "ch_1"}) != true {
		t.Fatal("channel rooms are shared")
	}
	if office.IsSharedRoom(models.OfficeRoomRef{Kind: models.OfficeRoomKindPersonal, Ref: "usr_2"}) != false {
		t.Fatal("personal offices are not shared")
	}
}
