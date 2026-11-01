package migrations

import (
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"gorm.io/gorm"
)

// OfficeIndexes creates the partial unique indexes described in the PRD
// (section 6.2). GORM's struct tags cannot express partial indexes, so they
// are created with plain SQL. Every statement is idempotent.
func OfficeIndexes(db *gorm.DB) {
	statements := []string{
		// Version history: one row per version for published/superseded layouts.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_office_layouts_version
			ON office_layouts (organisation_id, room_kind, ref_id, version)
			WHERE status IN ('published', 'superseded')`,

		// One live layout per room.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_office_layouts_published
			ON office_layouts (organisation_id, room_kind, ref_id)
			WHERE status = 'published'`,

		// One draft per admin per room.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_office_layouts_draft
			ON office_layouts (organisation_id, room_kind, ref_id, draft_owner_id)
			WHERE status = 'draft'`,

		// Fast lookups by room.
		`CREATE INDEX IF NOT EXISTS idx_office_layouts_room
			ON office_layouts (organisation_id, room_kind, ref_id, status)`,

		// Telemetry reads are always scoped to one organisation and a window
		// of time, so every event index leads with organisation_id.
		`CREATE INDEX IF NOT EXISTS idx_office_events_org_time
			ON office_events (organisation_id, occurred_at)`,

		`CREATE INDEX IF NOT EXISTS idx_office_events_org_kind_time
			ON office_events (organisation_id, kind, occurred_at)`,

		`CREATE INDEX IF NOT EXISTS idx_office_events_org_room_time
			ON office_events (organisation_id, room_kind, room_ref, occurred_at)`,

		`CREATE INDEX IF NOT EXISTS idx_office_visits_org_started
			ON office_visits (organisation_id, started_at)`,
	}

	for _, statement := range statements {
		db.Exec(statement)
	}
}

// RunOfficeMigrations wires the office tables into the shared migration run.
func RunOfficeMigrations(db *storage.Database) {
	if db == nil || db.Postgresql == nil {
		return
	}
	OfficeIndexes(db.Postgresql)
}
