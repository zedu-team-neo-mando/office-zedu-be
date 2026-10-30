package test_office

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// floorFakeDriver serves the tower's member query from a canned table and
// records every statement, so a test can prove that a tower whose members all
// kept their desk is never rewritten.
type floorFakeDriver struct {
	mu   sync.Mutex
	rows [][]driver.Value
	// statements are the statements the service prepared, in order.
	statements []string
	// updates are the arguments of every UPDATE, in order.
	updates [][]driver.Value
}

type floorFakeConn struct{ driver *floorFakeDriver }

func (c *floorFakeConn) Prepare(query string) (driver.Stmt, error) {
	c.driver.mu.Lock()
	c.driver.statements = append(c.driver.statements, query)
	c.driver.mu.Unlock()
	return &floorFakeStmt{query: query, driver: c.driver}, nil
}

func (c *floorFakeConn) Close() error              { return nil }
func (c *floorFakeConn) Begin() (driver.Tx, error) { return floorFakeTx{}, nil }

type floorFakeTx struct{}

func (floorFakeTx) Commit() error   { return nil }
func (floorFakeTx) Rollback() error { return nil }

type floorFakeStmt struct {
	query  string
	driver *floorFakeDriver
}

func (s *floorFakeStmt) Close() error  { return nil }
func (s *floorFakeStmt) NumInput() int { return -1 }

func (s *floorFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	s.driver.mu.Lock()
	defer s.driver.mu.Unlock()

	return &floorFakeRows{
		columns: []string{"user_id", "created_at", "floor_index", "floor_slot"},
		values:  s.driver.rows,
	}, nil
}

func (s *floorFakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.driver.mu.Lock()
	defer s.driver.mu.Unlock()

	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.query)), "UPDATE") {
		s.driver.updates = append(s.driver.updates, append([]driver.Value(nil), args...))
	}
	return driver.RowsAffected(1), nil
}

func (d *floorFakeDriver) Open(string) (driver.Conn, error) { return &floorFakeConn{driver: d}, nil }

func (d *floorFakeDriver) updateCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.updates)
}

type floorFakeRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *floorFakeRows) Columns() []string { return r.columns }
func (r *floorFakeRows) Close() error      { return nil }

func (r *floorFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func floorTestService(t *testing.T, driver *floorFakeDriver) *office.Service {
	t.Helper()

	name := "office-floors-" + t.Name()
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

	return office.New(&storage.Database{Postgresql: db}, utility.NewLogger())
}

func seatedMember(userID string, joinedAt time.Time, floorIndex, floorSlot any) []driver.Value {
	return []driver.Value{userID, joinedAt, floorIndex, floorSlot}
}

// Re-loading a tower that already matches its stored desks must not write: the
// bootstrap runs on every office load.
func TestSyncFloorAssignmentsWritesNothingWhenTheTowerIsStable(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	driver := &floorFakeDriver{rows: [][]driver.Value{
		seatedMember("usr_1", base, int64(models.OfficeFloorMember), int64(0)),
		seatedMember("usr_2", base.Add(time.Hour), int64(models.OfficeFloorMember), int64(1)),
	}}
	service := floorTestService(t, driver)

	assignments, err := service.SyncFloorAssignments("org-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := driver.updateCount(); got != 0 {
		t.Fatalf("a stable tower wrote %d rows on load", got)
	}
	if assignments.Floors["usr_2"] != models.OfficeFloorMember || assignments.Slots["usr_2"] != 1 {
		t.Fatalf("unexpected assignment %+v", assignments)
	}
}

// A member whose row has no desk yet is given the next free one, and only that
// row is written.
func TestSyncFloorAssignmentsSeatsAMemberWithoutADesk(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	driver := &floorFakeDriver{rows: [][]driver.Value{
		seatedMember("usr_1", base, int64(models.OfficeFloorMember), int64(0)),
		seatedMember("usr_2", base.Add(time.Hour), int64(models.OfficeFloorMember), nil),
	}}
	service := floorTestService(t, driver)

	assignments, err := service.SyncFloorAssignments("org-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := driver.updateCount(); got != 1 {
		t.Fatalf("expected exactly the seatless member to be written, got %d updates", got)
	}
	if assignments.Slots["usr_2"] != 1 || assignments.Floors["usr_2"] != models.OfficeFloorMember {
		t.Fatalf("the seatless member should take the next desk on their floor, got %+v", assignments)
	}
	if assignments.Slots["usr_1"] != 0 {
		t.Fatalf("the seated member moved to desk %d", assignments.Slots["usr_1"])
	}

	for _, args := range driver.updates {
		written := false
		for _, arg := range args {
			if arg == "usr_1" {
				t.Fatalf("the member who already had a desk was rewritten: %v", args)
			}
			if arg == "usr_2" {
				written = true
			}
		}
		if !written {
			t.Fatalf("unexpected update arguments %v", args)
		}
	}
}
