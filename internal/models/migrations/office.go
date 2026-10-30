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

		// The tower stores the desk each member holds so an office keeps its
		// place when a colleague leaves. AutoMigrate adds the column; this
		// index keeps the per-organisation layout read cheap.
		`CREATE INDEX IF NOT EXISTS idx_org_user_managements_floor_desk
			ON org_user_managements (organisation_id, floor_index, floor_slot)`,
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
