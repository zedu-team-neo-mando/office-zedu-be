package office

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
)

// FloorCapacity is the number of private offices on one member floor.
const FloorCapacity = models.OfficeFloorCapacity

// FloorMember is the part of a member the tower needs in order to place them.
type FloorMember struct {
	UserID   string
	JoinedAt time.Time
}

// FloorSlot is where one member sits in the tower.
type FloorSlot struct {
	UserID     string
	FloorIndex int
	Slot       int
}

// FloorDesk is the desk the tower has stored for a member. Either half can be
// unknown (-1): a row written before desks were stored knows its floor only.
type FloorDesk struct {
	FloorIndex int
	Slot       int
}

// FloorAssignments is the tower as a bootstrap derives it: which floor and
// which desk every active member holds.
type FloorAssignments struct {
	Floors map[string]int
	Slots  map[string]int
}

// UnknownFloor and UnknownSlot stand in for the half of a desk the database
// does not know yet.
const (
	UnknownFloor = -1
	UnknownSlot  = -1
)

// byJoinOrder copies the members and orders them the way the tower fills up:
// earliest joiner first, ties broken by user id so every client agrees.
func byJoinOrder(members []FloorMember) []FloorMember {
	ordered := make([]FloorMember, len(members))
	copy(ordered, members)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].JoinedAt.Equal(ordered[j].JoinedAt) {
			return ordered[i].UserID < ordered[j].UserID
		}
		return ordered[i].JoinedAt.Before(ordered[j].JoinedAt)
	})

	return ordered
}

// AssignFloorSlots places members top-down: the earliest joiners take the top
// member floor, and the floor below starts once the one above holds
// FloorCapacity offices. Equal join times are broken by user id so every client
// derives the same tower. It is the layout of a tower that has never stored a
// desk.
func AssignFloorSlots(members []FloorMember) []FloorSlot {
	ordered := byJoinOrder(members)

	slots := make([]FloorSlot, 0, len(ordered))
	for index, member := range ordered {
		slots = append(slots, FloorSlot{
			UserID:     member.UserID,
			FloorIndex: models.OfficeFloorMember + index/FloorCapacity,
			Slot:       index % FloorCapacity,
		})
	}

	return slots
}

// AssignStableFloorSlots keeps every desk a member already holds and hands the
// free desks to the members who have none, in join order. A departure therefore
// leaves an empty office behind instead of moving everybody who joined after the
// leaver, which is what a tower that doubles as a map of the organisation needs.
func AssignStableFloorSlots(members []FloorMember, stored map[string]FloorDesk) []FloorSlot {
	ordered := byJoinOrder(members)

	taken := make(map[int]bool, len(ordered))
	placed := make(map[string]bool, len(ordered))
	slots := make([]FloorSlot, 0, len(ordered))

	assign := func(userID string, floorIndex, slot int) {
		slots = append(slots, FloorSlot{UserID: userID, FloorIndex: floorIndex, Slot: slot})
		taken[deskKey(floorIndex, slot)] = true
		placed[userID] = true
	}

	// A stored desk wins. When two rows claim the same desk the earlier joiner
	// keeps it; the other one falls through to the passes below.
	for _, member := range ordered {
		desk := stored[member.UserID]
		if !deskKnown(desk) || taken[deskKey(desk.FloorIndex, desk.Slot)] {
			continue
		}
		assign(member.UserID, desk.FloorIndex, desk.Slot)
	}

	// A member whose floor is stored but whose desk is not keeps the floor, so
	// the run that introduces desks moves nobody between floors.
	for _, member := range ordered {
		if placed[member.UserID] || stored[member.UserID].FloorIndex < models.OfficeFloorMember {
			continue
		}
		if slot, free := freeSlotOnFloor(taken, stored[member.UserID].FloorIndex); free {
			assign(member.UserID, stored[member.UserID].FloorIndex, slot)
		}
	}

	// Everybody else, new joiners included, takes the lowest free desk.
	next := models.OfficeFloorMember * FloorCapacity
	for _, member := range ordered {
		if placed[member.UserID] {
			continue
		}
		for taken[next] {
			next++
		}
		assign(member.UserID, next/FloorCapacity, next%FloorCapacity)
		next++
	}

	return slots
}

// deskKey numbers every desk in the building so "the lowest free desk" can be
// found by walking one counter instead of every floor.
func deskKey(floorIndex, slot int) int {
	return floorIndex*FloorCapacity + slot
}

func deskKnown(desk FloorDesk) bool {
	return desk.FloorIndex >= models.OfficeFloorMember &&
		desk.Slot >= 0 && desk.Slot < FloorCapacity
}

func freeSlotOnFloor(taken map[int]bool, floorIndex int) (int, bool) {
	for slot := 0; slot < FloorCapacity; slot++ {
		if !taken[deskKey(floorIndex, slot)] {
			return slot, true
		}
	}

	return 0, false
}

// AreaRoomRef builds the composite reference of a per-floor extra, as
// "area:<floor>:<key>".
func AreaRoomRef(floorIndex int, key string) models.OfficeRoomRef {
	return models.OfficeRoomRef{
		Kind: models.OfficeRoomKindArea,
		Ref:  fmt.Sprintf("%d:%s", floorIndex, key),
	}
}

// ParseAreaRef splits "area:<floor>:<key>".
func ParseAreaRef(ref string) (int, string, bool) {
	floor, key, found := strings.Cut(ref, ":")
	if !found || key == "" {
		return 0, "", false
	}

	index, err := strconv.Atoi(floor)
	if err != nil || index < models.OfficeFloorMember {
		return 0, "", false
	}

	return index, key, true
}

// AreaRoomName is the human label for a floor extra.
func AreaRoomName(key string) string {
	switch key {
	case models.OfficeAreaVisitors:
		return "Visitors Area"
	case models.OfficeAreaPods:
		return "Open Pods"
	case models.OfficeAreaGames:
		return "Game Area"
	default:
		return "Area"
	}
}

// AreaRoomsForFloor returns the extras that make a member floor more than a row
// of desks. The general floor has its own shared rooms and no extras.
func AreaRoomsForFloor(floorIndex int) []models.OfficeRoomRef {
	if floorIndex < models.OfficeFloorMember {
		return nil
	}

	keys := []string{models.OfficeAreaVisitors, models.OfficeAreaPods, models.OfficeAreaGames}
	rooms := make([]models.OfficeRoomRef, 0, len(keys))
	for _, key := range keys {
		rooms = append(rooms, AreaRoomRef(floorIndex, key))
	}

	return rooms
}

// SyncFloorAssignments re-balances the tower for an organisation and stores the
// result on org_user_managements.floor_index and floor_slot. It runs on
// bootstrap, so a join or a leave is absorbed on the next load without a
// background job. Only members whose floor or desk actually moved are written.
func (s *Service) SyncFloorAssignments(orgID string) (*FloorAssignments, error) {
	type row struct {
		UserID     string
		CreatedAt  time.Time
		FloorIndex *int
		FloorSlot  *int
	}

	var rows []row
	err := s.Db.Postgresql.Table("org_user_managements").
		Select("user_id, created_at, floor_index, floor_slot").
		Where("organisation_id = ?", orgID).
		Where("is_deactivated = ?", false).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	members := make([]FloorMember, 0, len(rows))
	stored := make(map[string]FloorDesk, len(rows))
	for _, row := range rows {
		members = append(members, FloorMember{UserID: row.UserID, JoinedAt: row.CreatedAt})

		desk := FloorDesk{FloorIndex: UnknownFloor, Slot: UnknownSlot}
		if row.FloorIndex != nil {
			desk.FloorIndex = *row.FloorIndex
		}
		if row.FloorSlot != nil {
			desk.Slot = *row.FloorSlot
		}
		stored[row.UserID] = desk
	}

	assignments := &FloorAssignments{
		Floors: make(map[string]int, len(rows)),
		Slots:  make(map[string]int, len(rows)),
	}
	moved := make([]FloorSlot, 0)
	for _, slot := range AssignStableFloorSlots(members, stored) {
		assignments.Floors[slot.UserID] = slot.FloorIndex
		assignments.Slots[slot.UserID] = slot.Slot

		if stored[slot.UserID] != (FloorDesk{FloorIndex: slot.FloorIndex, Slot: slot.Slot}) {
			moved = append(moved, slot)
		}
	}

	if len(moved) == 0 {
		return assignments, nil
	}

	err = s.Db.Postgresql.Transaction(func(tx *gorm.DB) error {
		for _, slot := range moved {
			if err := tx.Table("org_user_managements").
				Where("organisation_id = ? AND user_id = ?", orgID, slot.UserID).
				Updates(map[string]any{
					"floor_index": slot.FloorIndex,
					"floor_slot":  slot.Slot,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return assignments, nil
}

// highestFloor is the topmost member floor in use, or the general floor when
// the organisation has no members.
func highestFloor(floors map[string]int) int {
	highest := models.OfficeFloorGeneral
	for _, floor := range floors {
		if floor > highest {
			highest = floor
		}
	}

	return highest
}

// memberFloor is the floor a member's office sits on, falling back to the top
// member floor before the tower has assigned one.
func memberFloor(floors map[string]int, userID string) int {
	if floor, ok := floors[userID]; ok && floor >= models.OfficeFloorMember {
		return floor
	}

	return models.OfficeFloorMember
}

// FloorDirectory lists the tower's storeys for the bootstrap payload: the
// general floor first, then every member floor in use.
func FloorDirectory(floors map[string]int) []models.OfficeFloorResponse {
	counts := make(map[int]int, len(floors))
	for _, floor := range floors {
		counts[floor]++
	}

	top := highestFloor(floors)
	directory := make([]models.OfficeFloorResponse, 0, top+1)
	directory = append(directory, models.OfficeFloorResponse{
		Index: models.OfficeFloorGeneral,
		Kind:  models.OfficeFloorKindGeneral,
		Name:  "General",
	})
	for floor := models.OfficeFloorMember; floor <= top; floor++ {
		directory = append(directory, models.OfficeFloorResponse{
			Index:     floor,
			Kind:      models.OfficeFloorKindMember,
			Name:      fmt.Sprintf("Floor %d", floor),
			Capacity:  FloorCapacity,
			Members:   counts[floor],
			HasPortal: true,
		})
	}

	return directory
}
