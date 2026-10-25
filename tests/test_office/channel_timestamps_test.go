package test_office

import (
	"os"
	"testing"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// This integration test uses temporary tables and rolls back all fixture data.
func channelTimestampDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("OFFICE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OFFICE_TEST_POSTGRES_DSN to run the PostgreSQL channel timestamp regression")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	tables := []string{
		"CREATE TEMP TABLE channels (id text PRIMARY KEY, name text, organisation_id text, is_private boolean, created_at timestamptz DEFAULT now(), deleted_at timestamptz)",
		"CREATE TEMP TABLE user_channels (channels_id text, user_id text)",
		"CREATE TEMP TABLE office_room_configs (organisation_id text, room_kind text, ref_id text, bounds jsonb, capacity int, enabled boolean)",
		"CREATE TEMP TABLE org_user_managements (user_id text, organisation_id text, role_id text, floor_index int, created_at timestamptz DEFAULT now(), is_deactivated boolean DEFAULT false)",
		"CREATE TEMP TABLE profiles (userid text, organisation_id text, display_name text, user_name text, full_name text, title text, avatar_url text, avatar_config jsonb, online boolean, text text, created_at timestamptz, deleted_at timestamptz)",
		"CREATE TEMP TABLE users (id text, name text, email text, deleted_at timestamptz)",
		"CREATE TEMP TABLE org_roles (id text, name text, deleted_at timestamptz)",
		"CREATE TEMP TABLE dm_channels (channel_id text, participant_id text, org_id text, user_id text, deleted_at timestamptz)",
		"CREATE TEMP TABLE channel_participants (channel_id text, user_id text, org_id text, deleted_at timestamptz)",
	}
	for _, statement := range tables {
		if err := tx.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Exec("INSERT INTO org_user_managements (user_id, organisation_id, role_id, floor_index) VALUES (?, ?, '', 1)", "member", "org-a").Error; err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		id      string
		org     string
		private bool
		deleted any
	}{
		{"public-null", "org-a", false, nil},
		{"public-zero", "org-a", false, time.Time{}},
		{"private-zero", "org-a", true, time.Time{}},
		{"joined-private-zero", "org-a", true, time.Time{}},
		{"deleted", "org-a", false, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)},
		{"other-org-zero", "org-b", false, time.Time{}},
		{"other-org-null", "org-b", false, nil},
	}
	for _, fixture := range fixtures {
		if fixture.id == "public-zero" {
			channel := models.Channels{ID: fixture.id, Name: fixture.id, OrganisationID: fixture.org}
			if err := tx.Select("id", "name", "organisation_id", "is_private", "created_at", "deleted_at").Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tx.Exec("INSERT INTO channels (id, name, organisation_id, is_private, deleted_at) VALUES (?, ?, ?, ?, ?)", fixture.id, fixture.id, fixture.org, fixture.private, fixture.deleted).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Exec("INSERT INTO user_channels VALUES (?, ?)", "joined-private-zero", "member").Error; err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestChannelDirectoryActiveTimestamps(t *testing.T) {
	service := officeService(t, channelTimestampDB(t))
	sets, err := service.AccessSets("org-a", "member")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := service.BuildDirectory("org-a", "member", sets)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{
		"public-null":         false,
		"public-zero":         false,
		"private-zero":        true,
		"joined-private-zero": false,
	}
	if len(directory.ChannelRoomMap) != len(expected) {
		t.Errorf("channel mappings: got %v, want %d active channels", directory.ChannelRoomMap, len(expected))
	}
	seen := make(map[string]bool)
	for _, room := range directory.Rooms {
		if room.Kind != models.OfficeRoomKindChannel {
			continue
		}
		locked, ok := expected[room.RefID]
		if !ok {
			t.Errorf("unexpected channel room: %s", room.ID)
			continue
		}
		seen[room.RefID] = true
		if room.ID != office.EncodeRoomID(models.OfficeRoomKindChannel, room.RefID) || directory.ChannelRoomMap[room.RefID] != room.ID {
			t.Errorf("incorrect channel mapping for %s", room.RefID)
		}
		if room.Name != room.RefID || room.FloorIndex != models.OfficeFloorGeneral || room.IsLocked != locked {
			t.Errorf("incorrect channel room: %+v", room)
		}
	}
	for id := range expected {
		if !seen[id] {
			t.Errorf("active channel missing from directory: %s", id)
		}
	}
	for _, id := range []string{"public-null", "public-zero"} {
		if _, ok := sets.PublicChannels[id]; !ok {
			t.Errorf("public channel missing from access sets: %s", id)
		}
	}
	if _, ok := sets.ChannelIDs["joined-private-zero"]; !ok {
		t.Error("joined private channel missing from access sets")
	}
	for _, id := range []string{"deleted", "other-org-zero", "other-org-null", "private-zero"} {
		if _, ok := sets.PublicChannels[id]; ok {
			t.Errorf("unexpected public access: %s", id)
		}
		if _, ok := sets.ChannelIDs[id]; ok {
			t.Errorf("unexpected membership access: %s", id)
		}
	}
}

func TestChannelAccessActiveTimestamps(t *testing.T) {
	service := officeService(t, channelTimestampDB(t))
	for _, tc := range []struct {
		id       string
		canEnter bool
	}{
		{"public-null", true},
		{"public-zero", true},
		{"private-zero", false},
		{"joined-private-zero", true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			access, err := service.CanAccess("org-a", "member", models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: tc.id})
			if err != nil {
				t.Fatal(err)
			}
			if access.CanEnter != tc.canEnter {
				t.Errorf("CanEnter = %v, want %v", access.CanEnter, tc.canEnter)
			}
		})
	}
}
