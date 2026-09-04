package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
)

func TestPublicLoadsKeepMetadataAndJournalConsistent(t *testing.T) {
	store, err := Open(t.Context(), StoreConfig{Path: filepath.Join(t.TempDir(), "sessions.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Create(t.Context(), t.TempDir(), session.Metadata{Title: "0"})
	if err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() {
		for i := 1; i <= 100; i++ {
			tx, err := store.db.BeginTx(t.Context(), nil)
			if err != nil {
				writer <- err
				return
			}
			typ, payload, err := session.EncodeEvent(session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: strconv.Itoa(i)}})
			if err == nil {
				_, err = tx.ExecContext(t.Context(), `INSERT INTO session_events(session_id,seq,type,payload,created_at) VALUES(?,?,?,?,?)`, record.ID, i, typ, payload, 0)
			}
			if err == nil {
				_, err = tx.ExecContext(t.Context(), `UPDATE sessions SET title=? WHERE id=?`, strconv.Itoa(i), record.ID)
			}
			if err == nil {
				err = tx.Commit()
			}
			_ = tx.Rollback()
			if err != nil {
				writer <- err
				return
			}
		}
		writer <- nil
	}()
	defer func() {
		if err := <-writer; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 100; i++ {
		var got session.Session
		var messages []llm.Message
		var found bool
		if i%2 == 0 {
			got, messages, found, err = store.Load(t.Context(), record.ID)
		} else {
			got, messages, found, err = store.Latest(t.Context(), record.WorkingDir)
		}
		if err != nil || !found {
			t.Fatalf("load = %v, %v", found, err)
		}
		if got.Title != strconv.Itoa(len(messages)) {
			t.Fatalf("mixed snapshot: title=%s messages=%d", got.Title, len(messages))
		}
	}
}

func TestReadSnapshotAllowsConcurrentWrites(t *testing.T) {
	for _, operation := range []string{"append", "switch", "delete"} {
		t.Run(operation, func(t *testing.T) {
			store, err := Open(t.Context(), StoreConfig{Path: filepath.Join(t.TempDir(), "sessions.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record, err := store.Create(t.Context(), t.TempDir(), session.Metadata{Model: config.Model{Name: "old"}})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			before, found, err := loadSession(t.Context(), tx, "WHERE id = ?", record.ID)
			if err != nil || !found {
				t.Fatalf("load: %v %v", found, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			switch operation {
			case "append":
				err = store.Append(ctx, record.ID, session.Event{Type: session.EventMessage, Message: llm.UserMessage{Text: "new"}})
			case "switch":
				err = store.SwitchModel(ctx, record.ID, session.Metadata{Model: config.Model{Name: "new"}}, session.Event{Type: session.EventModelChanged, Model: config.Model{Name: "new"}})
			case "delete":
				err = store.Delete(ctx, record.ID)
			}
			if err != nil {
				t.Fatalf("writer blocked by read snapshot: %v", err)
			}
			after, found, err := loadSession(t.Context(), tx, "WHERE id = ?", record.ID)
			if err != nil || !found || after.Model != before.Model {
				t.Fatalf("snapshot changed: %v %v %+v", found, err, after)
			}
			messages, _, err := loadMessages(t.Context(), tx, record.ID)
			if err != nil || len(messages) != 0 {
				t.Fatalf("snapshot messages = %v, %v", messages, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, latest := range []bool{false, true} {
				var got session.Session
				var messages []llm.Message
				var found bool
				if latest {
					got, messages, found, err = store.Latest(t.Context(), record.WorkingDir)
				} else {
					got, messages, found, err = store.Load(t.Context(), record.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if operation == "delete" {
					if found {
						t.Fatal("deleted session found")
					}
					continue
				}
				if !found {
					t.Fatal("session missing")
				}
				if operation == "append" && len(messages) != 1 {
					t.Fatal("append not visible")
				}
				if operation == "switch" && got.Model.Name != "new" {
					t.Fatal("model switch not visible")
				}
			}
		})
	}
}
