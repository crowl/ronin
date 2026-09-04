package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/session"
	_ "modernc.org/sqlite"
)

// Store persists session metadata and an append-only event journal in SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// StoreConfig configures a SQLite session store.
type StoreConfig struct {
	Path string
	Now  func() time.Time
}

// Open opens the database and applies the session schema.
func Open(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("database path must not be empty")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path %q: %w", cfg.Path, err)
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database %q: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect sqlite database %q: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite database %q: %w", path, err)
	}
	return &Store{db: db, now: cfg.Now}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Create(ctx context.Context, workingDir string, metadata session.Metadata) (session.Session, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return session.Session{}, err
	}
	id, err := newSessionID()
	if err != nil {
		return session.Session{}, err
	}
	title := metadata.Title
	if title == "" {
		title = "New session"
	}
	now := s.now().UTC()
	record := session.Session{
		Version:        session.Version,
		ID:             id,
		Title:          title,
		WorkingDir:     workingDir,
		CreatedAt:      now,
		UpdatedAt:      now,
		Model:          metadata.Model,
		ReasoningLevel: metadata.ReasoningLevel,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (
			id, working_dir, title, parent_id, model_provider, model_name,
			reasoning_level, created_at, updated_at
		) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)`,
		record.ID, record.WorkingDir, record.Title, nullable(record.Model.Provider),
		nullable(record.Model.Name), nullable(record.ReasoningLevel), unixTimestamp(now), unixTimestamp(now),
	)
	if err != nil {
		return session.Session{}, fmt.Errorf("create session %q: %w", id, err)
	}
	return record, nil
}

func (s *Store) Fork(ctx context.Context, parentID string, metadata session.Metadata, event session.Event) (session.Session, error) {
	if parentID == "" {
		return session.Session{}, errors.New("parent session id must not be empty")
	}
	parent, found, err := loadSession(ctx, s.db, `WHERE id = ?`, parentID)
	if err != nil {
		return session.Session{}, err
	}
	if !found {
		return session.Session{}, fmt.Errorf("parent session %q not found", parentID)
	}
	id, err := newSessionID()
	if err != nil {
		return session.Session{}, err
	}
	now := s.now().UTC()
	title := metadata.Title
	if title == "" {
		title = parent.Title
	}
	record := session.Session{
		Version: session.Version, ID: id, Title: title, WorkingDir: parent.WorkingDir,
		ParentID: parentID, CreatedAt: now, UpdatedAt: now, Model: metadata.Model,
		ReasoningLevel: metadata.ReasoningLevel, Cost: llm.SessionCost{Available: true},
	}
	eventType, payload, err := session.EncodeEvent(event)
	if err != nil {
		return session.Session{}, fmt.Errorf("encode fork event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return session.Session{}, fmt.Errorf("begin fork session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, working_dir, title, parent_id, model_provider, model_name, reasoning_level, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ID, record.WorkingDir, record.Title, parentID,
		nullable(record.Model.Provider), nullable(record.Model.Name), nullable(record.ReasoningLevel),
		unixTimestamp(now), unixTimestamp(now)); err != nil {
		return session.Session{}, fmt.Errorf("create fork session %q: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_events (session_id, seq, type, payload, created_at)
		VALUES (?, 1, ?, ?, ?)`, id, eventType, payload, unixTimestamp(now)); err != nil {
		return session.Session{}, fmt.Errorf("initialize fork session %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return session.Session{}, fmt.Errorf("commit fork session %q: %w", id, err)
	}
	return record, nil
}

func (s *Store) SwitchModel(ctx context.Context, sessionID string, metadata session.Metadata, event session.Event) error {
	if sessionID == "" {
		return errors.New("session id must not be empty")
	}
	eventType, payload, err := session.EncodeEvent(event)
	if err != nil {
		return fmt.Errorf("encode model change event: %w", err)
	}
	now := s.now().UTC()
	title := metadata.Title
	if title == "" {
		title = "New session"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model switch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO session_events (session_id, seq, type, payload, created_at)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ? FROM session_events WHERE session_id = ?`,
		sessionID, eventType, payload, unixTimestamp(now), sessionID)
	if err != nil {
		return fmt.Errorf("append model change to session %q: %w", sessionID, err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return fmt.Errorf("check model change event: %w", err)
		}
		return fmt.Errorf("append model change to session %q: no row inserted", sessionID)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE sessions SET title = ?, model_provider = ?, model_name = ?, reasoning_level = ?, updated_at = ? WHERE id = ?`,
		title, nullable(metadata.Model.Provider), nullable(metadata.Model.Name), nullable(metadata.ReasoningLevel), unixTimestamp(now), sessionID)
	if err != nil {
		return fmt.Errorf("update session %q model: %w", sessionID, err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return fmt.Errorf("check session model update: %w", err)
		}
		return fmt.Errorf("session %q not found", sessionID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model switch for session %q: %w", sessionID, err)
	}
	return nil
}

func (s *Store) Append(ctx context.Context, sessionID string, event session.Event) error {
	if sessionID == "" {
		return errors.New("session id must not be empty")
	}
	eventType, payload, err := session.EncodeEvent(event)
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now()
	}
	createdAt = createdAt.UTC()
	updatedAt := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append session event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO session_events (session_id, seq, type, payload, created_at)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?
		FROM session_events
		WHERE session_id = ?`, sessionID, eventType, payload, unixTimestamp(createdAt), sessionID)
	if err != nil {
		return fmt.Errorf("append event to session %q: %w", sessionID, err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check appended event for session %q: %w", sessionID, err)
	} else if n != 1 {
		return fmt.Errorf("append event to session %q: no row inserted", sessionID)
	}
	result, err = tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, unixTimestamp(updatedAt), sessionID)
	if err != nil {
		return fmt.Errorf("update session %q activity: %w", sessionID, err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check session %q activity update: %w", sessionID, err)
	} else if n != 1 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit event for session %q: %w", sessionID, err)
	}
	return nil
}

func (s *Store) UpdateMetadata(ctx context.Context, sessionID string, metadata session.Metadata) error {
	if sessionID == "" {
		return errors.New("session id must not be empty")
	}
	title := metadata.Title
	if title == "" {
		title = "New session"
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET title = ?, model_provider = ?, model_name = ?, reasoning_level = ?, updated_at = ?
		WHERE id = ?`, title, nullable(metadata.Model.Provider), nullable(metadata.Model.Name),
		nullable(metadata.ReasoningLevel), unixTimestamp(s.now().UTC()), sessionID)
	if err != nil {
		return fmt.Errorf("update session %q metadata: %w", sessionID, err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check session %q metadata update: %w", sessionID, err)
	} else if n != 1 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

func (s *Store) Load(ctx context.Context, sessionID string) (session.Session, []llm.Message, bool, error) {
	if sessionID == "" {
		return session.Session{}, nil, false, errors.New("session id must not be empty")
	}
	return s.loadSnapshot(ctx, `WHERE id = ?`, sessionID)
}

func (s *Store) Latest(ctx context.Context, workingDir string) (session.Session, []llm.Message, bool, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return session.Session{}, nil, false, err
	}
	return s.loadSnapshot(ctx, `WHERE working_dir = ? ORDER BY updated_at DESC, rowid DESC LIMIT 1`, workingDir)
}

func (s *Store) loadSnapshot(ctx context.Context, clause string, args ...any) (session.Session, []llm.Message, bool, error) {
	// modernc uses a deferred BEGIN for read-only transactions, even with
	// _txlock=immediate. WAL writers can proceed while this snapshot is read.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return session.Session{}, nil, false, fmt.Errorf("begin session snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, found, err := loadSession(ctx, tx, clause, args...)
	if err != nil || !found {
		return record, nil, found, err
	}
	messages, cost, err := loadMessages(ctx, tx, record.ID)
	if err != nil {
		return session.Session{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return session.Session{}, nil, false, fmt.Errorf("finish session snapshot: %w", err)
	}
	record.Cost = cost
	return record, messages, true, nil
}

func (s *Store) List(ctx context.Context, workingDir string) ([]session.Ref, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE working_dir = ?
		ORDER BY updated_at DESC, rowid DESC`, workingDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", workingDir, err)
	}
	defer rows.Close()

	var refs []session.Ref
	for rows.Next() {
		var ref session.Ref
		var createdAt, updatedAt int64
		if err := rows.Scan(&ref.ID, &ref.Title, &ref.ParentID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan session reference: %w", err)
		}
		ref.CreatedAt = timeFromTimestamp(createdAt)
		ref.UpdatedAt = timeFromTimestamp(updatedAt)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions for %q: %w", workingDir, err)
	}
	return refs, nil
}

func (s *Store) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session id must not be empty")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}
	return nil
}

func (s *Store) Clear(ctx context.Context, workingDir string) error {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE working_dir = ?`, workingDir); err != nil {
		return fmt.Errorf("clear sessions for %q: %w", workingDir, err)
	}
	return nil
}

type sessionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadSession(ctx context.Context, reader sessionReader, clause string, args ...any) (session.Session, bool, error) {
	row := reader.QueryRowContext(ctx, `
		SELECT id, working_dir, title, COALESCE(parent_id, ''),
		       COALESCE(model_provider, ''), COALESCE(model_name, ''),
		       COALESCE(reasoning_level, ''), created_at, updated_at
		FROM sessions `+clause, args...)
	var record session.Session
	var createdAt, updatedAt int64
	err := row.Scan(&record.ID, &record.WorkingDir, &record.Title, &record.ParentID,
		&record.Model.Provider, &record.Model.Name, &record.ReasoningLevel, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, fmt.Errorf("load session: %w", err)
	}
	record.Version = session.Version
	record.CreatedAt = timeFromTimestamp(createdAt)
	record.UpdatedAt = timeFromTimestamp(updatedAt)
	return record, true, nil
}

func loadMessages(ctx context.Context, reader sessionReader, sessionID string) ([]llm.Message, llm.SessionCost, error) {
	rows, err := reader.QueryContext(ctx, `
		SELECT seq, type, payload, created_at
		FROM session_events WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, llm.SessionCost{}, fmt.Errorf("load events for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	var events []session.Event
	cost := llm.SessionCost{Available: true}
	for rows.Next() {
		var seq, createdAt int64
		var eventType string
		var payload []byte
		if err := rows.Scan(&seq, &eventType, &payload, &createdAt); err != nil {
			return nil, llm.SessionCost{}, fmt.Errorf("scan event for session %q: %w", sessionID, err)
		}
		event, err := session.DecodeEvent(eventType, payload)
		if err != nil {
			return nil, llm.SessionCost{}, fmt.Errorf("decode event %d for session %q: %w", seq, sessionID, err)
		}
		event.Seq = seq
		event.CreatedAt = timeFromTimestamp(createdAt)
		if event.Type == session.EventMessage {
			if assistant, ok := event.Message.(llm.AssistantMessage); ok {
				if !assistant.Usage.Cost.Available {
					cost.Available = false
				} else {
					cost.Total += assistant.Usage.Cost.Total
				}
			}
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, llm.SessionCost{}, fmt.Errorf("load events for session %q: %w", sessionID, err)
	}
	return session.Reconstruct(events), cost, nil
}

func cleanWorkingDir(workingDir string) (string, error) {
	if strings.TrimSpace(workingDir) == "" {
		return "", errors.New("working directory must not be empty")
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", workingDir, err)
	}
	return filepath.Clean(abs), nil
}

func newSessionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("create session id: %w", err)
	}
	return "sess_" + hex.EncodeToString(bytes[:]), nil
}

const nanosecondTimestampThreshold int64 = 1_000_000_000_000_000

func unixTimestamp(t time.Time) int64 {
	return t.UnixNano()
}

func timeFromTimestamp(value int64) time.Time {
	if value > -nanosecondTimestampThreshold && value < nanosecondTimestampThreshold {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(0, value).UTC()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ session.Store = (*Store)(nil)
