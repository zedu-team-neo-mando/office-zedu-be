package test_office

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// directoryFakeDriver answers the queries BuildDirectory makes with a fixed
// organisation: one member, no channels, one 1:1 DM and one group DM the
// requester belongs to.
type directoryFakeDriver struct{}

type directoryFakeConn struct{}

func (c *directoryFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &directoryFakeStmt{query: query}, nil
}
func (c *directoryFakeConn) Close() error              { return nil }
func (c *directoryFakeConn) Begin() (driver.Tx, error) { return nil, io.EOF }

type directoryFakeStmt struct{ query string }

func (s *directoryFakeStmt) Close() error  { return nil }
func (s *directoryFakeStmt) NumInput() int { return -1 }

func (s *directoryFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, io.EOF
}

func (s *directoryFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.rowsFor(s.query), nil
}

func (s *directoryFakeStmt) rowsFor(query string) *accessFakeRows {
	switch {
	case strings.Contains(query, `FROM "dm_channels"`):
		return &accessFakeRows{
			columns: []string{"channel_id", "participant_id"},
			values:  [][]driver.Value{{"dm-1", "usr-peer"}},
		}
	case strings.Contains(query, "LEFT JOIN LATERAL"):
		return &accessFakeRows{
			columns: []string{"user_id", "display_name", "username", "title", "avatar_url", "tint", "role", "online", "status_text"},
			values:  [][]driver.Value{{"usr-1", "Ada", "ada", "", "", "", "member", false, ""}},
		}
	case strings.Contains(query, "FROM org_user_managements"):
		return &accessFakeRows{
			columns: []string{"user_id", "created_at", "floor_index"},
			values:  [][]driver.Value{{"usr-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), int64(models.OfficeFloorMember)}},
		}
	case strings.Contains(query, `FROM "channel_participants"`):
		// The group DM's membership row. Binding it to the requester is the
		// bug: the old query returned only the requester's own row, so the
		// group channel was mapped to personal:usr-1.
		return &accessFakeRows{
			columns: []string{"channel_id", "user_id"},
			values:  [][]driver.Value{{"grp-1", "usr-1"}},
		}
	default:
		return &accessFakeRows{}
	}
}

func (d directoryFakeDriver) Open(string) (driver.Conn, error) { return &directoryFakeConn{}, nil }

func directoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	name := "office-directory-" + t.Name()
	sql.Register(name, directoryFakeDriver{})

	conn, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("failed to open the fake database: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:             conn,
		DriverName:       name,
		WithoutReturning: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("failed to build the fake database: %v", err)
	}

	return db
}

// A 1:1 DM maps its channel to the peer's office. A group DM has no single
// other participant, so it must not be bound to anybody's office - least of all
// the requester's own, which made opening your office start a group chat.
func TestDirectoryDMOfficeMapSkipsGroupDMs(t *testing.T) {
	service := office.New(&storage.Database{Postgresql: directoryTestDB(t)}, utility.NewLogger())

	directory, err := service.BuildDirectory("org-1", "usr-1", office.AccessSets{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := directory.DMOfficeMap["dm-1"]; got != "personal:usr-peer" {
		t.Fatalf("1:1 DM map: got %q, want %q", got, "personal:usr-peer")
	}

	if _, ok := directory.DMOfficeMap["grp-1"]; ok {
		t.Fatal("a group DM must not be bound to an office")
	}

	for channelID, roomID := range directory.DMOfficeMap {
		if roomID == "personal:usr-1" {
			t.Fatalf("DM %q is bound to the requester's own office", channelID)
		}
	}
}
