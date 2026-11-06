package test_office

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
)

func occupantHash(t *testing.T, occupant models.OfficeOccupantResponse) string {
	t.Helper()
	raw, err := json.Marshal(occupant)
	if err != nil {
		t.Fatalf("failed to encode occupant %s: %v", occupant.UserID, err)
	}
	return string(raw)
}

func TestOccupancyPatternIsScopedToTheOrganisation(t *testing.T) {
	pattern := office.OccupancyPattern("org_1")

	if !strings.HasPrefix(pattern, "office:presence:") {
		t.Fatalf("pattern %q does not name presence keys", pattern)
	}
	if !strings.HasSuffix(pattern, ":*") {
		t.Fatalf("pattern %q does not cover every room of the organisation", pattern)
	}
	if !strings.Contains(pattern, "org_1:") {
		t.Fatalf("pattern %q does not carry the organisation", pattern)
	}
}

func TestOccupancyPatternEscapesGlobCharacters(t *testing.T) {
	// An organisation value with a glob character would otherwise widen the
	// scan onto other organisations' presence keys.
	pattern := office.OccupancyPattern("org*1")

	if !strings.Contains(pattern, `org\*1`) {
		t.Fatalf("pattern %q does not escape the glob character", pattern)
	}
}

func TestMergeOccupantsKeepsTheLatestRoomPerPerson(t *testing.T) {
	now := time.Now().UTC()
	older := models.OfficeOccupantResponse{
		UserID:    "usr_1",
		RoomID:    "lobby",
		UpdatedAt: now.Add(-2 * time.Minute),
	}
	newer := models.OfficeOccupantResponse{
		UserID:    "usr_1",
		RoomID:    "channel:ch_1",
		UpdatedAt: now.Add(-10 * time.Second),
	}

	got := office.MergeOccupants(map[string]map[string]string{
		"office:presence:org_1:lobby": {
			"usr_1": occupantHash(t, older),
		},
		"office:presence:org_1:channel:ch_1": {
			"usr_1": occupantHash(t, newer),
		},
	}, now)

	if len(got) != 1 {
		t.Fatalf("got %d occupants, want 1", len(got))
	}
	if got[0].RoomID != "channel:ch_1" {
		t.Fatalf("kept room %q, want the later one", got[0].RoomID)
	}
}

func TestMergeOccupantsDropsExpiredPresence(t *testing.T) {
	now := time.Now().UTC()
	stale := models.OfficeOccupantResponse{
		UserID:    "usr_1",
		RoomID:    "lobby",
		UpdatedAt: now.Add(-30 * time.Minute),
	}
	live := models.OfficeOccupantResponse{
		UserID:    "usr_2",
		RoomID:    "lobby",
		UpdatedAt: now.Add(-time.Minute),
	}

	got := office.MergeOccupants(map[string]map[string]string{
		"office:presence:org_1:lobby": {
			"usr_1": occupantHash(t, stale),
			"usr_2": occupantHash(t, live),
		},
	}, now)

	if len(got) != 1 || got[0].UserID != "usr_2" {
		t.Fatalf("got %+v, want only the live occupant", got)
	}
}

func TestMergeOccupantsSkipsMalformedEntries(t *testing.T) {
	now := time.Now().UTC()
	valid := models.OfficeOccupantResponse{
		UserID:    "usr_1",
		RoomID:    "lobby",
		UpdatedAt: now.Add(-time.Minute),
	}

	got := office.MergeOccupants(map[string]map[string]string{
		"office:presence:org_1:lobby": {
			"usr_1": occupantHash(t, valid),
			"usr_2": "not json",
			"usr_3": `{"roomId":"lobby"}`,
		},
	}, now)

	if len(got) != 1 || got[0].UserID != "usr_1" {
		t.Fatalf("got %+v, want only the readable occupant", got)
	}
}

func TestMergeOccupantsOrdersByRoomThenUser(t *testing.T) {
	now := time.Now().UTC()
	hashes := map[string]map[string]string{
		"office:presence:org_1:lobby":          {},
		"office:presence:org_1:personal:usr_2": {},
	}
	for _, id := range []string{"usr_3", "usr_1", "usr_2"} {
		room := "personal:usr_2"
		if id == "usr_3" {
			room = "lobby"
		}
		occupant := models.OfficeOccupantResponse{
			UserID:    id,
			RoomID:    room,
			UpdatedAt: now.Add(-time.Minute),
		}
		hashes["office:presence:org_1:"+room][id] = occupantHash(t, occupant)
	}

	got := office.MergeOccupants(hashes, now)

	want := []string{"usr_3", "usr_1", "usr_2"}
	if len(got) != len(want) {
		t.Fatalf("got %d occupants, want %d", len(got), len(want))
	}
	for index, userID := range want {
		if got[index].UserID != userID {
			t.Fatalf("position %d is %q, want %q", index, got[index].UserID, userID)
		}
	}
}

func TestMergeOccupantsCapsTheBuilding(t *testing.T) {
	now := time.Now().UTC()
	hashes := map[string]map[string]string{"office:presence:org_1:lobby": {}}

	for index := 0; index < office.OccupancyEntryCap+25; index++ {
		occupant := models.OfficeOccupantResponse{
			UserID:    fmt.Sprintf("usr_%04d", index),
			RoomID:    "lobby",
			UpdatedAt: now.Add(-time.Minute),
		}
		hashes["office:presence:org_1:lobby"][occupant.UserID] = occupantHash(t, occupant)
	}

	got := office.MergeOccupants(hashes, now)

	if len(got) != office.OccupancyEntryCap {
		t.Fatalf("got %d occupants, want the cap of %d", len(got), office.OccupancyEntryCap)
	}
}

func TestOccupancyWithoutRedisReturnsAnEmptyBuilding(t *testing.T) {
	// No Redis client at all is the local and CI-less case: an empty building,
	// never an error.
	service := office.New(&storage.Database{}, nil)

	got, err := service.Occupancy(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d occupants, want none", len(got))
	}
}

func TestOccupancyWithoutOrganisationSkipsTheRead(t *testing.T) {
	// The client is configured but points nowhere. An empty organisation must
	// return before the first command, so this never dials and never errors.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	service := office.New(&storage.Database{Redis: client}, nil)

	got, err := service.Occupancy(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d occupants, want none", len(got))
	}
}

