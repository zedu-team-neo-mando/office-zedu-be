package test_office

import (
	"fmt"
	"testing"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
)

func members(count int) []office.FloorMember {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	people := make([]office.FloorMember, 0, count)
	for index := 0; index < count; index++ {
		people = append(people, office.FloorMember{
			UserID:   fmt.Sprintf("usr_%03d", index),
			JoinedAt: base.Add(time.Duration(index) * time.Hour),
		})
	}

	return people
}

func TestAssignFloorSlotsEmpty(t *testing.T) {
	if slots := office.AssignFloorSlots(nil); len(slots) != 0 {
		t.Fatalf("expected no slots, got %d", len(slots))
	}
}

func TestAssignFloorSlotsFillsTopFloorFirst(t *testing.T) {
	slots := office.AssignFloorSlots(members(39))
	if len(slots) != 39 {
		t.Fatalf("expected 39 slots, got %d", len(slots))
	}

	for _, slot := range slots {
		if slot.FloorIndex != models.OfficeFloorMember {
			t.Fatalf("member %s landed on floor %d, want the top member floor", slot.UserID, slot.FloorIndex)
		}
	}

	// The earliest joiner takes the first desk on the top floor.
	if slots[0].UserID != "usr_000" || slots[0].Slot != 0 {
		t.Fatalf("expected usr_000 in slot 0, got %s in slot %d", slots[0].UserID, slots[0].Slot)
	}
}

func TestAssignFloorSlotsFullFloor(t *testing.T) {
	slots := office.AssignFloorSlots(members(models.OfficeFloorCapacity))
	last := slots[len(slots)-1]

	if last.FloorIndex != models.OfficeFloorMember {
		t.Fatalf("the fortieth member should still fit the top floor, got floor %d", last.FloorIndex)
	}
	if last.Slot != models.OfficeFloorCapacity-1 {
		t.Fatalf("expected the last slot on the floor, got %d", last.Slot)
	}
}

func TestAssignFloorSlotsOverflowsDownwards(t *testing.T) {
	slots := office.AssignFloorSlots(members(models.OfficeFloorCapacity + 1))

	first := slots[0]
	overflow := slots[len(slots)-1]

	if first.FloorIndex != models.OfficeFloorMember || first.Slot != 0 {
		t.Fatalf("the earliest joiner must stay on the top floor, got floor %d slot %d", first.FloorIndex, first.Slot)
	}
	if overflow.FloorIndex != models.OfficeFloorMember+1 || overflow.Slot != 0 {
		t.Fatalf("expected the newest joiner on the floor below, got floor %d slot %d", overflow.FloorIndex, overflow.Slot)
	}
}

func TestAssignFloorSlotsKeepsEarlyJoinersOnTopAsTowerGrows(t *testing.T) {
	first := office.AssignFloorSlots(members(3))
	tower := office.AssignFloorSlots(members(85))

	if tower[0].FloorIndex != first[0].FloorIndex {
		t.Fatalf("growing the tower moved the first member from floor %d to %d", first[0].FloorIndex, tower[0].FloorIndex)
	}

	floors := map[int]bool{}
	for _, slot := range tower {
		floors[slot.FloorIndex] = true
	}
	if len(floors) != 3 {
		t.Fatalf("85 members should need 3 member floors, got %d: %v", len(floors), floors)
	}
}

func TestAssignFloorSlotsIsStableForEqualJoinTimes(t *testing.T) {
	joined := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	slots := office.AssignFloorSlots([]office.FloorMember{
		{UserID: "usr_b", JoinedAt: joined},
		{UserID: "usr_a", JoinedAt: joined},
	})

	if slots[0].UserID != "usr_a" {
		t.Fatalf("equal join times must break by user id, got %s first", slots[0].UserID)
	}
}

func TestAssignFloorSlotsIgnoresInputOrder(t *testing.T) {
	people := members(5)
	people[0], people[4] = people[4], people[0]

	slots := office.AssignFloorSlots(people)
	if slots[0].UserID != "usr_000" {
		t.Fatalf("the earliest joiner must lead regardless of input order, got %s", slots[0].UserID)
	}
}

// desksOf turns an assignment into the map the tower stores.
func desksOf(slots []office.FloorSlot) map[string]office.FloorDesk {
	desks := make(map[string]office.FloorDesk, len(slots))
	for _, slot := range slots {
		desks[slot.UserID] = office.FloorDesk{FloorIndex: slot.FloorIndex, Slot: slot.Slot}
	}

	return desks
}

// A departure must leave an empty office behind, not slide everybody who joined
// after the leaver one desk back and pull the first office of the floor below up.
func TestAssignStableFloorSlotsKeepsDesksWhenAMemberLeaves(t *testing.T) {
	people := members(models.OfficeFloorCapacity + 3)
	before := office.AssignStableFloorSlots(people, nil)

	// The first member of the top floor leaves the organisation.
	after := office.AssignStableFloorSlots(people[1:], desksOf(before))
	afterDesks := desksOf(after)

	for _, slot := range before[1:] {
		if got := afterDesks[slot.UserID]; got != (office.FloorDesk{FloorIndex: slot.FloorIndex, Slot: slot.Slot}) {
			t.Fatalf(
				"member %s moved from floor %d desk %d to floor %d desk %d because somebody else left",
				slot.UserID, slot.FloorIndex, slot.Slot, got.FloorIndex, got.Slot,
			)
		}
	}

	// The desk the leaver held stays empty instead of being handed on.
	for _, slot := range after {
		if slot.FloorIndex == models.OfficeFloorMember && slot.Slot == 0 {
			t.Fatalf("member %s took the leaver's desk; it should stay empty until the next joiner", slot.UserID)
		}
	}
}

// Rows written before desks existed only carry their floor. Introducing desks
// must not move anybody between floors.
func TestAssignStableFloorSlotsKeepsStoredFloorsWithoutDesks(t *testing.T) {
	people := members(models.OfficeFloorCapacity + 4)
	stored := make(map[string]office.FloorDesk, len(people))
	for index, member := range people {
		stored[member.UserID] = office.FloorDesk{
			FloorIndex: models.OfficeFloorMember + index/models.OfficeFloorCapacity,
			Slot:       office.UnknownSlot,
		}
	}

	for _, slot := range office.AssignStableFloorSlots(people, stored) {
		if want := stored[slot.UserID].FloorIndex; slot.FloorIndex != want {
			t.Fatalf("member %s moved from floor %d to floor %d while desks were introduced", slot.UserID, want, slot.FloorIndex)
		}
	}

	// Desks on a floor are unique and start at zero.
	seen := map[string]int{}
	for _, slot := range office.AssignStableFloorSlots(people, stored) {
		key := fmt.Sprintf("%d:%d", slot.FloorIndex, slot.Slot)
		if seen[key] != 0 {
			t.Fatalf("floor %d desk %d was given to two members", slot.FloorIndex, slot.Slot)
		}
		seen[key] = 1
	}
}

// Two rows claiming one desk keep the earlier joiner there; the later one is
// placed elsewhere instead of sharing the office.
func TestAssignStableFloorSlotsBreaksDeskCollisionsByJoinOrder(t *testing.T) {
	people := members(3)
	stored := map[string]office.FloorDesk{
		"usr_000": {FloorIndex: models.OfficeFloorMember, Slot: 7},
		"usr_001": {FloorIndex: models.OfficeFloorMember, Slot: 7},
		"usr_002": {FloorIndex: models.OfficeFloorMember, Slot: 7},
	}

	desks := desksOf(office.AssignStableFloorSlots(people, stored))

	if got := desks["usr_000"]; got.Slot != 7 {
		t.Fatalf("the earliest joiner must keep desk 7, got %d", got.Slot)
	}
	if got := desks["usr_001"]; got.Slot == 7 {
		t.Fatal("the second member was given the same desk as the first")
	}
	if desks["usr_001"] != (office.FloorDesk{FloorIndex: models.OfficeFloorMember, Slot: 0}) {
		t.Fatalf("the second member should take the lowest free desk, got %+v", desks["usr_001"])
	}
}

// New joiners fill the gaps a tower already has before a floor below is used.
func TestAssignStableFloorSlotsGivesNewJoinersTheLowestFreeDesk(t *testing.T) {
	people := members(models.OfficeFloorCapacity + 1)
	before := office.AssignStableFloorSlots(people, nil)

	// A new member joins a tower whose top floor is full.
	newcomer := office.FloorMember{
		UserID:   "usr_new",
		JoinedAt: people[len(people)-1].JoinedAt.Add(time.Hour),
	}
	after := desksOf(office.AssignStableFloorSlots(append(people, newcomer), desksOf(before)))

	if got := after["usr_new"]; got != (office.FloorDesk{FloorIndex: models.OfficeFloorMember + 1, Slot: 1}) {
		t.Fatalf("the newcomer should take the next free desk on the floor below, got %+v", got)
	}
}

func TestAreaRoomsForFloor(t *testing.T) {
	if rooms := office.AreaRoomsForFloor(models.OfficeFloorGeneral); rooms != nil {
		t.Fatalf("the general floor has no extras, got %d", len(rooms))
	}

	rooms := office.AreaRoomsForFloor(2)
	if len(rooms) != 3 {
		t.Fatalf("expected three extras per member floor, got %d", len(rooms))
	}

	want := []string{"2:visitors", "2:pods", "2:games"}
	for index, room := range rooms {
		if room.Kind != models.OfficeRoomKindArea || room.Ref != want[index] {
			t.Fatalf("unexpected area room %s:%s, want %s", room.Kind, room.Ref, want[index])
		}
	}
}

func TestParseAreaRef(t *testing.T) {
	floor, key, ok := office.ParseAreaRef("3:visitors")
	if !ok || floor != 3 || key != "visitors" {
		t.Fatalf("expected floor 3 visitors, got %d %q %v", floor, key, ok)
	}

	for _, ref := range []string{"", "visitors", "0:visitors", "x:visitors", "2:"} {
		if _, _, ok := office.ParseAreaRef(ref); ok {
			t.Fatalf("expected %q to be rejected", ref)
		}
	}
}

func TestParseRoomIDAcceptsAreaRooms(t *testing.T) {
	ref, err := office.ParseRoomID("area:2:pods")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Kind != models.OfficeRoomKindArea || ref.Ref != "2:pods" {
		t.Fatalf("unexpected ref %+v", ref)
	}

	if _, err := office.ParseRoomID("area:not-a-floor"); err == nil {
		t.Fatal("expected an invalid area reference to be rejected")
	}
}

func TestRoomFloorIndexForArea(t *testing.T) {
	ref := office.AreaRoomRef(3, models.OfficeAreaGames)
	if floor := office.RoomFloorIndex(ref); floor != 3 {
		t.Fatalf("expected floor 3, got %d", floor)
	}

	if floor := office.RoomFloorIndex(models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: "ch_1"}); floor != models.OfficeFloorGeneral {
		t.Fatalf("channel rooms live on the general floor, got %d", floor)
	}
}

func TestFloorDirectory(t *testing.T) {
	directory := office.FloorDirectory(map[string]int{
		"usr_1": models.OfficeFloorMember,
		"usr_2": models.OfficeFloorMember,
		"usr_3": models.OfficeFloorMember + 1,
	})

	if len(directory) != 3 {
		t.Fatalf("expected the general floor plus two member floors, got %d", len(directory))
	}

	general := directory[0]
	if general.Index != models.OfficeFloorGeneral || general.Kind != models.OfficeFloorKindGeneral {
		t.Fatalf("the general floor must come first, got %+v", general)
	}

	top := directory[1]
	if top.Members != 2 || top.Capacity != models.OfficeFloorCapacity || !top.HasPortal {
		t.Fatalf("unexpected top member floor %+v", top)
	}

	if directory[2].Members != 1 || directory[2].Index != models.OfficeFloorMember+1 {
		t.Fatalf("unexpected second member floor %+v", directory[2])
	}
}
