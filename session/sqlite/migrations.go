package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 1

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}

	for version < schemaVersion {
		nextVersion := version + 1
		var migration string
		switch nextVersion {
		case 1:
			migration = schema
		default:
			return fmt.Errorf("missing schema migration from version %d to %d", version, nextVersion)
		}
		if err := applyMigration(ctx, db, nextVersion, migration); err != nil {
			return err
		}
		version = nextVersion
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int, migration string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration %d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		return fmt.Errorf("apply schema version %d: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("record schema version %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema version %d: %w", version, err)
	}
	return nil
}
