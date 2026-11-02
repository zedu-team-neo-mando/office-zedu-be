package migrations

import (
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"gorm.io/gorm"
)

// FixDeletedAtColumns repairs legacy soft-delete columns left by the
// `column: deleted_at` tag typo. The leading space in that tag created a column
// literally named " deleted_at" with NOT NULL; no code writes it any more, so
// every insert into the table leaves a NOT NULL column unset and fails with
// SQLSTATE 23502. webhooks is the table where that still breaks organisation
// creation.
//
// The repair is split into separate statements on purpose. Dropping NOT NULL is
// what unblocks the insert, so it runs on its own: a later failure can only roll
// back its own statement, never that repair. Folding the legacy column into the
// real deleted_at then runs table by table, each one guarded, so a table that
// cannot be repaired (a dependent view blocks the drop, for instance) is skipped
// with a warning instead of aborting the rest.
//
// Only base tables are considered. Views cannot have NOT NULL dropped or columns
// dropped, and a view that happens to expose the typo name is not ours to alter.
func FixDeletedAtColumns(db *gorm.DB) {
	const legacyColumns = `
		SELECT cols.table_name
		FROM information_schema.columns cols
		JOIN information_schema.tables tabs
			ON tabs.table_schema = cols.table_schema
			AND tabs.table_name = cols.table_name
		WHERE cols.table_schema = current_schema()
			AND cols.column_name = ' deleted_at'
			AND tabs.table_type = 'BASE TABLE'`

	statements := []string{
		`DO $$
		DECLARE legacy RECORD;
		BEGIN
			FOR legacy IN` + legacyColumns + `
			LOOP
				EXECUTE format(
					'ALTER TABLE %I ALTER COLUMN %I DROP NOT NULL',
					legacy.table_name, ' deleted_at'
				);
			END LOOP;
		END $$;`,

		`DO $$
		DECLARE legacy RECORD;
		BEGIN
			FOR legacy IN` + legacyColumns + `
			LOOP
				BEGIN
					IF EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_schema = current_schema()
							AND table_name = legacy.table_name
							AND column_name = 'deleted_at'
					) THEN
						EXECUTE format(
							'UPDATE %I SET deleted_at = %I WHERE deleted_at IS NULL AND %I > ''0001-01-02''',
							legacy.table_name, ' deleted_at', ' deleted_at'
						);
						EXECUTE format(
							'ALTER TABLE %I DROP COLUMN %I',
							legacy.table_name, ' deleted_at'
						);
					ELSE
						EXECUTE format(
							'ALTER TABLE %I RENAME COLUMN %I TO deleted_at',
							legacy.table_name, ' deleted_at'
						);
					END IF;

					EXECUTE format(
						'ALTER TABLE %I ALTER COLUMN deleted_at DROP NOT NULL',
						legacy.table_name
					);
				EXCEPTION WHEN OTHERS THEN
					RAISE WARNING 'deleted_at repair skipped for %: %', legacy.table_name, SQLERRM;
				END;
			END LOOP;
		END $$;`,

		// groups also carries a legacy " updated_at" column from the same typo.
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
					AND table_name = 'groups'
					AND column_name = ' updated_at'
			) THEN
				ALTER TABLE groups RENAME COLUMN " updated_at" TO updated_at;
			END IF;
		END $$;`,
	}

	for _, stmt := range statements {
		_ = db.Exec(stmt)
	}
}

func RunAllMigrations(db *storage.Database) {
	if db == nil || db.Postgresql == nil {
		return
	}

	// 1. First ensure legacy columns with leading space typos are repaired
	FixDeletedAtColumns(db.Postgresql)

	// 2. verification migration
	MigrateModels(db.Postgresql, AuthMigrationModels(), AlterColumnModels())
	OfficeIndexes(db.Postgresql)

}

func MigrateModels(db *gorm.DB, models []any, AlterColums []AlterColumn) {
	_ = db.AutoMigrate(models...)

	// for _, alter := range AlterColums {
	// 	if alter.Column != "" {
	// 		alter.AddColumn(db)
	// 	}
	// }
}
