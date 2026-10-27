package test_office

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// accessFakeDriver answers the office access queries from a canned table so a
// channel lookup can be made to find nothing (an id that does not exist in the
// organisation) while membership and role lookups still succeed.
type accessFakeDriver struct {
	// channelValues are the rows the "FROM channels" lookup returns. Nil
	// simulates an unknown, foreign or soft-deleted channel id.
	channelValues [][]driver.Value
	// participantValues are the other members the group-DM query returns.
	participantValues [][]driver.Value
}

type accessFakeConn struct{ driver accessFakeDriver }

func (c *accessFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &accessFakeStmt{query: query, driver: c.driver}, nil
}
func (c *accessFakeConn) Close() error              { return nil }
func (c *accessFakeConn) Begin() (driver.Tx, error) { return nil, io.EOF }

type accessFakeStmt struct {
	query  string
	driver accessFakeDriver
}

func (s *accessFakeStmt) Close() error  { return nil }
func (s *accessFakeStmt) NumInput() int { return -1 }

func (s *accessFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, io.EOF
}

// Query builds a fresh rows set every time so a cached prepared statement can
// never answer a second call with an exhausted result set.
func (s *accessFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.rowsFor(s.query), nil
}

func (s *accessFakeStmt) rowsFor(query string) *accessFakeRows {
	membership := [][]driver.Value{
		{"mem-1", "org-1", "usr-1", "role-member"},
	}
	role := [][]driver.Value{
		{"role-member", "member"},
	}
	notFound := [][]driver.Value(nil)

	switch {
	case strings.Contains(query, "org_user_managements"):
		return &accessFakeRows{
			columns: []string{"id", "organisation_id", "user_id", "role_id"},
			values:  membership,
		}
	case strings.Contains(query, "org_roles"):
		return &accessFakeRows{columns: []string{"id", "name"}, values: role}
	case strings.Contains(query, `FROM "user_channels"`):
		return &accessFakeRows{columns: []string{"count"}, values: [][]driver.Value{{int64(0)}}}
	case strings.Contains(query, `FROM "channels"`):
		return &accessFakeRows{columns: []string{"id", "is_private"}, values: s.driver.channelValues}
	case strings.Contains(query, `FROM "channel_participants"`):
		if strings.Contains(query, "user_id <>") {
			return &accessFakeRows{columns: []string{"user_id"}, values: s.driver.participantValues}
		}
		// The old query filtered by user_id = requester, so the only row it
		// could return is the requester's own membership row.
		return &accessFakeRows{
			columns: []string{"channel_id", "user_id"},
			values:  [][]driver.Value{{"grp-1", "usr-1"}},
		}
	default:
		return &accessFakeRows{values: notFound}
	}
}

func (d accessFakeDriver) Open(string) (driver.Conn, error) { return &accessFakeConn{driver: d}, nil }

type accessFakeRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *accessFakeRows) Columns() []string { return r.columns }
func (r *accessFakeRows) Close() error      { return nil }

func (r *accessFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func accessTestDB(t *testing.T, driver accessFakeDriver) *gorm.DB {
	t.Helper()

	name := "office-access-" + t.Name()
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

func accessService(t *testing.T, db *gorm.DB) *office.Service {
	t.Helper()

	return office.New(&storage.Database{Postgresql: db}, utility.NewLogger())
}

// A channel id with no non-deleted row in the organisation (an unknown id, a
// channel that belongs to another organisation, or a soft-deleted channel) must
// be a locked door: PRD 6.5 grants entry to channel rooms by membership or by
// the channel being a public channel of this organisation. CanAccessFromSets
// already denies it; CanAccess must agree.
func TestCanAccessDeniesChannelOutsideTheOrganisation(t *testing.T) {
	ref := models.OfficeRoomRef{
		Kind: models.OfficeRoomKindChannel,
		Ref:  "11111111-2222-3333-4444-555555555555",
	}

	setsAccess := office.CanAccessFromSets(office.AccessSets{}, ref, "usr-1")
	if setsAccess.CanEnter {
		t.Fatal("CanAccessFromSets must deny a channel that is not in the organisation")
	}

	db := accessTestDB(t, accessFakeDriver{})

	access, err := accessService(t, db).CanAccess("org-1", "usr-1", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if access.CanEnter {
		t.Fatalf("CanAccess granted entry to channel %q although no channel with this id exists in the organisation: "+
			"the record lookup fails, is_private stays false, and the request falls through to CanEnter = true "+
			"(services/office/office.go, channel branch). The sets-based path denies it, so the two paths disagree",
			ref.Ref)
	}
}

// Sanity guard for the harness above: a real public channel of the same
// organisation stays open to every member (the rule PR #13 shipped), so the
// denial in the test above cannot come from a broken fake database.
func TestCanAccessAllowsOwnOrganisationPublicChannel(t *testing.T) {
	db := accessTestDB(t, accessFakeDriver{
		channelValues: [][]driver.Value{{"ch-public", false}},
	})

	access, err := accessService(t, db).CanAccess("org-1", "usr-1", models.OfficeRoomRef{
		Kind: models.OfficeRoomKindChannel,
		Ref:  "ch-public",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !access.CanEnter {
		t.Fatal("a public channel of the requester's own organisation must stay enterable")
	}
}

// The other members of a group DM are DM participants: their personal offices
// must be enterable, exactly like a 1:1 DM peer.
func TestAccessSetsIncludesGroupDMParticipants(t *testing.T) {
	db := accessTestDB(t, accessFakeDriver{
		participantValues: [][]driver.Value{{"usr-peer"}},
	})

	sets, err := accessService(t, db).AccessSets("org-1", "usr-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := sets.DMParticipants["usr-peer"]; !ok {
		t.Fatal("a group-DM peer must be in the DM participant set")
	}
	if _, ok := sets.DMParticipants["usr-1"]; ok {
		t.Fatal("the requester must not be added as their own DM participant")
	}

	access := office.CanAccessFromSets(sets, models.OfficeRoomRef{
		Kind: models.OfficeRoomKindPersonal,
		Ref:  "usr-peer",
	}, "usr-1")
	if !access.CanEnter {
		t.Fatal("a group-DM peer's personal office must be enterable")
	}
}
