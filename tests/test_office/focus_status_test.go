package test_office

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
)

func TestPresenceStatusValidation(t *testing.T) {
	validate := validator.New()
	for _, status := range []string{"", "online", "idle", "away", "call", "dnd"} {
		if err := validate.Struct(models.OfficeRoomEnterRequest{Status: status}); err != nil {
			t.Fatal(err)
		}
		if err := validate.Struct(models.OfficePresenceUpdateRequest{Status: status, Position: &models.OfficeVector{}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"busy", "DND", "admin"} {
		if validate.Struct(models.OfficeRoomEnterRequest{Status: status}) == nil {
			t.Fatalf("accepted %q", status)
		}
		if validate.Struct(models.OfficePresenceUpdateRequest{Status: status, Position: &models.OfficeVector{}}) == nil {
			t.Fatalf("accepted update %q", status)
		}
	}
	if validate.Struct(models.OfficePresenceUpdateRequest{Status: "dnd"}) == nil {
		t.Fatal("accepted missing position")
	}
}

func TestLegacyPresenceStatusOptional(t *testing.T) {
	var entry models.OfficeRoomEnterRequest
	if err := json.Unmarshal([]byte(`{}`), &entry); err != nil {
		t.Fatal(err)
	}
	var update models.OfficePresenceUpdateRequest
	if err := json.Unmarshal([]byte(`{"position":{"x":1,"y":0,"z":1},"rotation":0}`), &update); err != nil {
		t.Fatal(err)
	}
	if err := validator.New().Struct(update); err != nil {
		t.Fatal(err)
	}
	if entry.Status != "" || update.Status != "" {
		t.Fatal("legacy requests unexpectedly set status")
	}
}

func TestOfficeStatusStoredInRedis(t *testing.T) {
	addr := os.Getenv("OFFICE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set OFFICE_TEST_REDIS_ADDR to run the Redis integration")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	org := "office-status-test-" + time.Now().Format("150405.000000000")
	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindFocus, Ref: "1"}
	roomID := office.EncodeRoomID(ref.Kind, ref.Ref)
	key := "office:presence:" + org + ":" + roomID
	throttle := "office:presence_update:" + org + ":" + roomID + ":tester"
	t.Cleanup(func() { client.Del(ctx, key, throttle) })
	service := office.New(&storage.Database{Redis: client}, nil)
	if err := service.JoinRoom(models.OfficePresenceCommand{OrganisationID: org, Room: ref, UserID: "tester", Presence: models.OfficeOccupantResponse{UserID: "tester", Status: "dnd"}}); err != nil {
		t.Fatal(err)
	}
	assertStatus := func(want string) {
		t.Helper()
		rows, err := service.Occupants(org, ref)
		if err != nil || len(rows) != 1 {
			t.Fatalf("occupants: %v %v", rows, err)
		}
		if rows[0].Status != want {
			t.Fatalf("status %q, want %q", rows[0].Status, want)
		}
	}
	assertStatus("dnd")
	update := models.OfficePresenceUpdateCommand{OrganisationID: org, Room: ref, UserID: "tester", Position: models.OfficeVector{X: 1, Z: 1}}
	if err := service.UpdatePresence(update); err != nil {
		t.Fatal(err)
	}
	assertStatus("dnd")
	client.Del(ctx, throttle)
	update.Status = "away"
	if err := service.UpdatePresence(update); err != nil {
		t.Fatal(err)
	}
	assertStatus("away")
	if err := service.LeaveRoom(org, "tester", ref); err != nil {
		t.Fatal(err)
	}
	rows, err := service.Occupants(org, ref)
	if err != nil || len(rows) != 0 {
		t.Fatalf("presence survived leave: %v %v", rows, err)
	}
}
