package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func TestOpenConfiguresAndMigratesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ronin.db")
	store, err := Open(t.Context(), StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	var journalMode string
	var foreignKeys, busyTimeout, version int
	if err := store.db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if err := store.db.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if err := store.db.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if err := store.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if journalMode != "wal" || foreignKeys != 1 || busyTimeout != 5000 || version != schemaVersion {
		t.Fatalf("database configuration = journal_mode %q, foreign_keys %d, busy_timeout %d, user_version %d", journalMode, foreignKeys, busyTimeout, version)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(t.Context(), StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("Open(existing) error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close(existing) error = %v", err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ronin.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(t.Context(), StoreConfig{Path: path})
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open() error = %v, want newer schema error", err)
	}
}

func TestDeleteCascadesSessionEvents(t *testing.T) {
	store := openInternalStore(t)
	created, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Append(t.Context(), created.ID, session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "persisted"}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := store.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_events WHERE session_id = ?`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count session events: %v", err)
	}
	if count != 0 {
		t.Fatalf("event count after Delete() = %d, want 0", count)
	}
}

func TestLoadRejectsInvalidJournalEvents(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   string
		want      string
	}{
		{name: "unknown event type", eventType: "future_event", payload: `{}`, want: `unsupported event type "future_event"`},
		{name: "malformed payload", eventType: "user_message", payload: `{`, want: "unexpected end of JSON input"},
		{name: "mismatched payload", eventType: "assistant_message", payload: `{"type":"user","text":"hello"}`, want: "does not match payload type"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openInternalStore(t)
			created, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			_, err = store.db.ExecContext(t.Context(), `
				INSERT INTO session_events (session_id, seq, type, payload, created_at)
				VALUES (?, 1, ?, ?, ?)`, created.ID, test.eventType, []byte(test.payload), unixTimestamp(time.Now()))
			if err != nil {
				t.Fatalf("insert invalid event: %v", err)
			}

			_, _, _, err = store.Load(t.Context(), created.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func openInternalStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), StoreConfig{Path: filepath.Join(t.TempDir(), "ronin.db")})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}
