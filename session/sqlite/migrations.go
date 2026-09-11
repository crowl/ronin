package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 2

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}

	if version == schemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("incompatible database schema version %d (expected %d); select a new session database", version, schemaVersion)
	}
	return applyMigration(ctx, db, schemaVersion, schema)
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
