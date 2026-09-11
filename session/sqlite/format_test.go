package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsOldFormatWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `PRAGMA user_version = 1; CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES ('keep');`); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), StoreConfig{Path: path})
	if err == nil {
		store.Close()
		t.Fatal("old format accepted")
	}
	if !strings.Contains(err.Error(), "incompatible database schema") {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM retained`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "keep" {
		t.Fatal("old data changed")
	}
	var version int
	if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("old version changed to %d", version)
	}
}
