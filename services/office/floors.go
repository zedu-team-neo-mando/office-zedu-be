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

// AssignFloorSlots places members top-down: the earliest joiners take the top
// member floor, and the floor below starts once the one above holds
// FloorCapacity offices. Equal join times are broken by user id so every client
// derives the same tower.
func AssignFloorSlots(members []FloorMember) []FloorSlot {
	ordered := make([]FloorMember, len(members))
	copy(ordered, members)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].JoinedAt.Equal(ordered[j].JoinedAt) {
			return ordered[i].UserID < ordered[j].UserID
		}
		return ordered[i].JoinedAt.Before(ordered[j].JoinedAt)
	})

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

// PortalRoomRef builds the reference of a storey's portal, as "portal:<floor>".
// A portal is circulation, not a room: it is the doorway between a member floor
// and the channels floor.
func PortalRoomRef(floorIndex int) models.OfficeRoomRef {
	return models.OfficeRoomRef{
		Kind: models.OfficeRoomKindPortal,
		Ref:  strconv.Itoa(floorIndex),
	}
}

// ParsePortalRef splits "portal:<floor>".
func ParsePortalRef(ref string) (int, bool) {
	index, err := strconv.Atoi(strings.TrimSpace(ref))
	if err != nil || index < models.OfficeFloorGeneral {
		return 0, false
	}

	return index, true
}

// PortalRoomName is what the doorway promises on the other side.
func PortalRoomName(floorIndex int) string {
	if floorIndex == models.OfficeFloorGeneral {
		return "Private floors"
	}

	return "Channels"
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
// result on org_user_managements.floor_index. It runs on bootstrap, so a join or
// a leave re-balances the tower on the next load without a background job. Only
// members whose floor actually moved are written.
func (s *Service) SyncFloorAssignments(orgID string) (map[string]int, error) {
	type row struct {
		UserID     string
		CreatedAt  time.Time
		FloorIndex *int
	}

	var rows []row
	err := s.Db.Postgresql.Table("org_user_managements").
		Select("user_id, created_at, floor_index").
		Where("organisation_id = ?", orgID).
		Where("is_deactivated = ?", false).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	members := make([]FloorMember, 0, len(rows))
	current := make(map[string]*int, len(rows))
	for _, row := range rows {
		members = append(members, FloorMember{UserID: row.UserID, JoinedAt: row.CreatedAt})
		current[row.UserID] = row.FloorIndex
	}

	slots := AssignFloorSlots(members)
	floors := make(map[string]int, len(slots))
	stale := make([]FloorSlot, 0)
	for _, slot := range slots {
		floors[slot.UserID] = slot.FloorIndex
		if stored := current[slot.UserID]; stored == nil || *stored != slot.FloorIndex {
			stale = append(stale, slot)
		}
	}

	if len(stale) == 0 {
		return floors, nil
	}

	err = s.Db.Postgresql.Transaction(func(tx *gorm.DB) error {
		for _, slot := range stale {
			if err := tx.Table("org_user_managements").
				Where("organisation_id = ? AND user_id = ?", orgID, slot.UserID).
				Update("floor_index", slot.FloorIndex).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return floors, nil
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
