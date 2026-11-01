package test_office

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// archiveRecorder records every statement the office services prepare and
// answers the channel queries with one active public channel. Every other
// query gets an empty result set, which the services treat as "no rows".
type archiveRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *archiveRecorder) record(query string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.queries = append(r.queries, query)
}

// channelQueries returns the window onto the channels table: the queries that
// decide which chat channels are office rooms.
func (r *archiveRecorder) channelQueries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var found []string
	for _, query := range r.queries {
		if strings.Contains(query, `FROM "channels"`) || strings.Contains(query, "FROM channels") {
			found = append(found, query)
		}
	}

	return found
}

// rowsFor always returns a fresh row set: a cached prepared statement must
// never answer a second call with an exhausted result set.
func (r *archiveRecorder) rowsFor(query string) *archiveRows {
	switch {
	case strings.Contains(query, `FROM "org_user_managements"`):
		return &archiveRows{
			columns: []string{"user_id", "organisation_id", "role_id", "is_deactivated"},
			values:  [][]driver.Value{{"usr-1", "org-1", "", false}},
		}
	case strings.Contains(query, "channels.name"):
		return &archiveRows{
			columns: []string{"id", "name", "is_private", "joined"},
			values:  [][]driver.Value{{"ch-active", "Active", false, false}},
		}
	case strings.Contains(query, "AS joined"):
		return &archiveRows{
			columns: []string{"id", "is_private", "joined"},
			values:  [][]driver.Value{{"ch-active", false, false}},
		}
	case strings.Contains(query, `FROM "channels"`) || strings.Contains(query, "FROM channels"):
		return &archiveRows{
			columns: []string{"id", "is_private"},
			values:  [][]driver.Value{{"ch-active", false}},
		}
	}

	return &archiveRows{}
}

type archiveConn struct{ recorder *archiveRecorder }

func (c *archiveConn) Prepare(query string) (driver.Stmt, error) {
	c.recorder.record(query)
	return &archiveStmt{recorder: c.recorder, query: query}, nil
}
func (c *archiveConn) Close() error              { return nil }
func (c *archiveConn) Begin() (driver.Tx, error) { return archiveTx{}, nil }

type archiveTx struct{}

func (archiveTx) Commit() error   { return nil }
func (archiveTx) Rollback() error { return nil }

type archiveStmt struct {
	recorder *archiveRecorder
	query    string
}

func (s *archiveStmt) Close() error  { return nil }
func (s *archiveStmt) NumInput() int { return -1 }

func (s *archiveStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (s *archiveStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.recorder.rowsFor(s.query), nil
}

type archiveRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *archiveRows) Columns() []string { return r.columns }
func (r *archiveRows) Close() error      { return nil }

func (r *archiveRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

type archiveDriver struct{ recorder *archiveRecorder }

func (d archiveDriver) Open(string) (driver.Conn, error) {
	return &archiveConn{recorder: d.recorder}, nil
}

func archiveTestService(t *testing.T, recorder *archiveRecorder) *office.Service {
	t.Helper()

	name := "office-archive-" + t.Name()
	sql.Register(name, archiveDriver{recorder: recorder})

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

// An archived channel disappears from the 2D channel list, so it must not stay
// open as an office room. Every window the office opens onto the channels
// table - the directory, the access sets and the access check - has to carry
// the same archive predicate.
func TestOfficeChannelQueriesExcludeArchivedChannels(t *testing.T) {
	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindChannel, Ref: "ch-archived"}

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, service *office.Service)
	}{
		{"access sets", func(t *testing.T, service *office.Service) {
			if _, err := service.AccessSets("org-1", "usr-1"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}},
		{"access check", func(t *testing.T, service *office.Service) {
			if _, err := service.CanAccess("org-1", "usr-1", ref); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}},
		{"directory", func(t *testing.T, service *office.Service) {
			if _, err := service.BuildDirectory("org-1", "usr-1", office.AccessSets{}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &archiveRecorder{}
			tc.run(t, archiveTestService(t, recorder))

			queries := recorder.channelQueries()
			if len(queries) == 0 {
				t.Fatal("the office never read the channels table; the test would pass vacuously")
			}
			for _, query := range queries {
				if !strings.Contains(query, "channels.archived = FALSE") {
					t.Fatalf("channel query keeps archived channels:\n%s", query)
				}
			}
		})
	}
}

// A live public channel is still enterable, so the archive predicate cannot be
// hiding every channel.
func TestPublicChannelStaysEnterableWithTheArchivePredicate(t *testing.T) {
	service := archiveTestService(t, &archiveRecorder{})

	access, err := service.CanAccess("org-1", "usr-1", models.OfficeRoomRef{
		Kind: models.OfficeRoomKindChannel,
		Ref:  "ch-active",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !access.CanEnter {
		t.Fatal("a public channel of the organisation must stay enterable")
	}

	sets, err := service.AccessSets("org-1", "usr-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := sets.PublicChannels["ch-active"]; !ok {
		t.Fatal("a public channel must stay in the office access sets")
	}
}
