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

// boundsFakeDriver answers the queries GetRoom and BuildDirectory issue from
// one canned dataset, so both paths derive the same building from the same
// rows and any difference in their output is a code difference, not a data
// difference.
type boundsFakeDriver struct{}

type boundsFakeConn struct{ driver boundsFakeDriver }

func (c *boundsFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &boundsFakeStmt{query: query}, nil
}
func (c *boundsFakeConn) Close() error              { return nil }
func (c *boundsFakeConn) Begin() (driver.Tx, error) { return nil, io.EOF }

type boundsFakeStmt struct{ query string }

func (s *boundsFakeStmt) Close() error { return nil }
func (s *boundsFakeStmt) NumInput() int {
	return -1
}

func (s *boundsFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, io.EOF
}

// Query builds a fresh rows set every time so a cached prepared statement can
// never answer a second call with an exhausted result set.
func (s *boundsFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.rowsFor(s.query), nil
}

func (s *boundsFakeStmt) rowsFor(query string) *boundsFakeRows {
	joinedAt := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	switch {
	case strings.Contains(query, "office_room_configs"):
		// No overrides: both paths fall back to defaultBounds.
		return &boundsFakeRows{columns: []string{"id"}}

	case strings.Contains(query, "floor_index"):
		// SyncFloorAssignments. Both members already sit on the floor the
		// assignment derives (OfficeFloorMember), so no UPDATE is attempted.
		return &boundsFakeRows{
			columns: []string{"user_id", "created_at", "floor_index"},
			values: [][]driver.Value{
				{"usr-1", joinedAt, int64(models.OfficeFloorMember)},
				{"usr-2", joinedAt.Add(time.Hour), int64(models.OfficeFloorMember)},
			},
		}

	case strings.Contains(query, "org_user_managements AS m"):
		// organisationMembers: usr-1 first, usr-2 second, so BuildDirectory
		// gives usr-2's personal office index 1.
		return &boundsFakeRows{
			columns: []string{"user_id", "display_name", "username", "title", "avatar_url", "tint", "role", "online", "status_text"},
			values: [][]driver.Value{
				{"usr-1", "Alice", "alice", "", "", "", "", false, ""},
				{"usr-2", "Bob", "bob", "", "", "", "", false, ""},
			},
		}

	case strings.Contains(query, "dm_channels"):
		// dmOfficeMap 1:1 query: no direct messages, the map stays empty.
		return &boundsFakeRows{columns: []string{"channel_id", "participant_id"}}

	case strings.Contains(query, "channel_participants"):
		// dmOfficeMap group query: no group DMs.
		return &boundsFakeRows{columns: []string{"channel_id", "user_id"}}

	case strings.Contains(query, `FROM "channels"`):
		// channelsForUser, in creation order: ch-1 then ch-2, so
		// BuildDirectory gives ch-2's room index 1.
		return &boundsFakeRows{
			columns: []string{"id", "name", "is_private", "joined"},
			values: [][]driver.Value{
				{"ch-1", "general", false, true},
				{"ch-2", "random", false, true},
			},
		}

	default:
		return &boundsFakeRows{}
	}
}

func (d boundsFakeDriver) Open(string) (driver.Conn, error) {
	return &boundsFakeConn{driver: d}, nil
}

type boundsFakeRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *boundsFakeRows) Columns() []string { return r.columns }
func (r *boundsFakeRows) Close() error      { return nil }

func (r *boundsFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func boundsTestDB(t *testing.T, driver boundsFakeDriver) *gorm.DB {
	t.Helper()

	name := "office-getroom-bounds-" + t.Name()
	sql.Register(name, driver)

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

// GetRoom must return the same bounds BuildDirectory returns for the same
// room. BuildDirectory passes each room's position in its kind list as the
// index to defaultBounds (services/office/rooms.go), while GetRoom hardcodes
// index := 0 (services/office/rooms.go:277), so every room after the first of
// its kind reports a footprint that contradicts the bootstrap payload the
// client already has. Expected bounds come from BuildDirectory, the reference
// implementation of the building.
func TestGetRoomBoundsMatchBuildDirectory(t *testing.T) {
	db := boundsTestDB(t, boundsFakeDriver{})
	svc := office.New(&storage.Database{Postgresql: db}, utility.NewLogger())

	sets := office.AccessSets{
		ChannelIDs:     map[string]struct{}{"ch-2": {}},
		DMParticipants: map[string]struct{}{"usr-2": {}},
	}

	directory, err := svc.BuildDirectory("org-1", "usr-1", sets)
	if err != nil {
		t.Fatalf("failed to build the directory: %v", err)
	}

	boundsOf := func(kind, ref string) (models.OfficeBounds, bool) {
		wantID := office.EncodeRoomID(kind, ref)
		for _, room := range directory.Rooms {
			if room.ID == wantID {
				return room.Bounds, true
			}
		}
		return models.OfficeBounds{}, false
	}

	cases := []struct {
		name string
		kind string
		ref  string
	}{
		{"meeting 2", models.OfficeRoomKindMeeting, "2"},
		{"focus 2", models.OfficeRoomKindFocus, "2"},
		{"channel room", models.OfficeRoomKindChannel, "ch-2"},
		{"personal office", models.OfficeRoomKindPersonal, "usr-2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := boundsOf(tc.kind, tc.ref)
			if !ok {
				t.Fatalf("BuildDirectory did not list room %q", office.EncodeRoomID(tc.kind, tc.ref))
			}

			room, err := svc.GetRoom("org-1", "usr-1", sets, models.OfficeRoomRef{Kind: tc.kind, Ref: tc.ref})
			if err != nil {
				t.Fatalf("GetRoom failed: %v", err)
			}
			if room == nil {
				t.Fatal("GetRoom returned no room")
			}

			if room.Bounds != want {
				t.Errorf("GetRoom returned bounds %+v for %s but BuildDirectory returns %+v: "+
					"GetRoom passes index := 0 to defaultBounds (services/office/rooms.go:277, used at services/office/rooms.go:327) "+
					"instead of the room's position in its kind list as BuildDirectory does (services/office/rooms.go:168-243)",
					room.Bounds, office.EncodeRoomID(tc.kind, tc.ref), want)
			}
		})
	}
}

// GetRoom must survive meeting/focus references that are not a valid one-based
// room number. roomIndexFromRef (services/office/rooms.go) falls back to index 0
// for "0", non-numeric and negative refs, and subtracts one without clamping
// for a number larger than the room count (the directory would use the same
// index if the organisation really had that many rooms). Meeting room N sits at
// X = 18 + (N-1)*7 in defaultBounds.
func TestGetRoomIndexEdgeCases(t *testing.T) {
	db := boundsTestDB(t, boundsFakeDriver{})
	svc := office.New(&storage.Database{Postgresql: db}, utility.NewLogger())

	cases := []struct {
		name  string
		ref   string
		wantX float64
	}{
		{"zero falls back to first room", "0", 18},
		{"non-numeric falls back to first room", "abc", 18},
		{"negative falls back to first room", "-1", 18},
		{"number above room count is not clamped", "99", 18 + 98*7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			room, err := svc.GetRoom("org-1", "usr-1", office.AccessSets{},
				models.OfficeRoomRef{Kind: models.OfficeRoomKindMeeting, Ref: tc.ref})
			if err != nil {
				t.Fatalf("GetRoom failed: %v", err)
			}
			if room == nil {
				t.Fatal("GetRoom returned no room")
			}

			if room.Bounds.X != tc.wantX {
				t.Errorf("GetRoom bounds for meeting:%s = %+v, want X %v", tc.ref, room.Bounds, tc.wantX)
			}
		})
	}
}
