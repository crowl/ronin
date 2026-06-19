package fs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crowl/ronin/fsutil"
	"github.com/crowl/ronin/session"
)

type Store struct {
	dir string
	now func() time.Time
}

type StoreConfig struct {
	Dir string
	Now func() time.Time
}

func NewStore(cfg StoreConfig) *Store {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now() }
	}
	return &Store{
		dir: filepath.Join(cfg.Dir, "sessions"),
		now: cfg.Now,
	}
}

func (s *Store) LoadActive(workingDir string) (session.Session, bool, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return session.Session{}, false, err
	}

	workspace, ok, err := s.loadWorkspace(workingDir)
	if err != nil {
		return session.Session{}, false, err
	}
	if !ok || workspace.ActiveSessionID == "" {
		return session.Session{}, false, nil
	}

	record, err := s.loadSession(workspace.ActiveSessionID)
	if err != nil {
		return session.Session{}, false, err
	}
	if record.WorkingDir != workingDir {
		return session.Session{}, false, fmt.Errorf("session %q working directory %q does not match %q", record.ID, record.WorkingDir, workingDir)
	}

	return record, true, nil
}

func (s *Store) Load(id string) (session.Session, bool, error) {
	if id == "" {
		return session.Session{}, false, errors.New("session id must not be empty")
	}
	record, err := s.loadSession(id)
	if errors.Is(err, os.ErrNotExist) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, err
	}
	return record, true, nil
}

func (s *Store) Create(workingDir string, metadata session.Metadata) (session.Session, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return session.Session{}, err
	}

	id, err := newSessionID()
	if err != nil {
		return session.Session{}, err
	}

	now := s.now().UTC()
	title := metadata.Title
	if title == "" {
		title = "New session"
	}

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
	if err := s.Save(record); err != nil {
		return session.Session{}, err
	}

	return record, nil
}

func (s *Store) Save(record session.Session) error {
	record, err := s.prepareSession(record)
	if err != nil {
		return err
	}

	workspace, ok, err := s.loadWorkspace(record.WorkingDir)
	if err != nil {
		return err
	}
	if !ok {
		workspace = session.Workspace{
			Version:    session.Version,
			WorkingDir: record.WorkingDir,
		}
	}

	workspace.Version = session.Version
	workspace.WorkingDir = record.WorkingDir
	workspace.ActiveSessionID = record.ID
	workspace.Sessions = upsertRef(workspace.Sessions, session.Ref{
		ID:        record.ID,
		Title:     record.Title,
		ParentID:  record.ParentID,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	})

	if err := s.writeSession(record); err != nil {
		return err
	}
	if err := s.writeWorkspace(workspace); err != nil {
		return err
	}

	return nil
}

func (s *Store) List(workingDir string) ([]session.Ref, error) {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return nil, err
	}
	workspace, ok, err := s.loadWorkspace(workingDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return append([]session.Ref(nil), workspace.Sessions...), nil
}

func (s *Store) Delete(id string) error {
	if id == "" {
		return errors.New("session id must not be empty")
	}
	record, err := s.loadSession(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	if err := os.Remove(s.recordPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session record %q: %w", id, err)
	}

	workspace, ok, err := s.loadWorkspace(record.WorkingDir)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	workspace.Sessions = removeRef(workspace.Sessions, id)
	if workspace.ActiveSessionID == id {
		workspace.ActiveSessionID = latestRefID(workspace.Sessions)
	}
	return s.writeWorkspace(workspace)
}

func (s *Store) Clear(workingDir string) error {
	workingDir, err := cleanWorkingDir(workingDir)
	if err != nil {
		return err
	}

	workspace, ok, err := s.loadWorkspace(workingDir)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	for _, summary := range workspace.Sessions {
		if summary.ID == "" {
			continue
		}
		if err := os.Remove(s.recordPath(summary.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove session record %q: %w", summary.ID, err)
		}
	}
	if err := os.Remove(s.workspacePath(workingDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workspace session %q: %w", workingDir, err)
	}

	return nil
}

func (s *Store) prepareSession(record session.Session) (session.Session, error) {
	if record.ID == "" {
		return session.Session{}, errors.New("session id must not be empty")
	}
	workingDir, err := cleanWorkingDir(record.WorkingDir)
	if err != nil {
		return session.Session{}, err
	}
	record.Version = session.Version
	record.WorkingDir = workingDir
	if record.Title == "" {
		record.Title = "New session"
	}
	now := s.now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	return record, nil
}

func (s *Store) loadWorkspace(workingDir string) (session.Workspace, bool, error) {
	path := s.workspacePath(workingDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return session.Workspace{}, false, nil
	}
	if err != nil {
		return session.Workspace{}, false, fmt.Errorf("read workspace session %q: %w", path, err)
	}

	var workspace session.Workspace
	if err := json.Unmarshal(data, &workspace); err != nil {
		return session.Workspace{}, false, fmt.Errorf("parse workspace session %q: %w", path, err)
	}
	if workspace.Version != session.Version {
		return session.Workspace{}, false, fmt.Errorf("unsupported workspace session version %d in %q", workspace.Version, path)
	}
	if workspace.WorkingDir != workingDir {
		return session.Workspace{}, false, fmt.Errorf("workspace session working directory %q does not match %q", workspace.WorkingDir, workingDir)
	}

	return workspace, true, nil
}

func (s *Store) loadSession(id string) (session.Session, error) {
	path := s.recordPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return session.Session{}, fmt.Errorf("read session record %q: %w", path, err)
	}

	record, err := decode(data)
	if err != nil {
		return session.Session{}, fmt.Errorf("parse session record %q: %w", path, err)
	}
	if record.ID != id {
		return session.Session{}, fmt.Errorf("session record id %q does not match %q", record.ID, id)
	}

	return record, nil
}

func (s *Store) writeWorkspace(workspace session.Workspace) error {
	data, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(s.workspacePath(workspace.WorkingDir), data)
}

func (s *Store) writeSession(record session.Session) error {
	data, err := encode(record)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.recordPath(record.ID), data)
}

func (s *Store) workspacePath(workingDir string) string {
	return filepath.Join(s.dir, "workspaces", workspaceKey(workingDir)+".json")
}

func (s *Store) recordPath(id string) string {
	return filepath.Join(s.dir, "records", id+".json")
}

func cleanWorkingDir(workingDir string) (string, error) {
	if workingDir == "" {
		return "", errors.New("working directory must not be empty")
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", workingDir, err)
	}
	return filepath.Clean(abs), nil
}

func workspaceKey(workingDir string) string {
	sum := sha256.Sum256([]byte(workingDir))
	return hex.EncodeToString(sum[:])[:16]
}

func newSessionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("create session id: %w", err)
	}
	return "sess_" + hex.EncodeToString(bytes[:]), nil
}

func upsertRef(summaries []session.Ref, summary session.Ref) []session.Ref {
	for i := range summaries {
		if summaries[i].ID == summary.ID {
			summaries[i] = summary
			return summaries
		}
	}
	return append(summaries, summary)
}

func removeRef(summaries []session.Ref, id string) []session.Ref {
	kept := summaries[:0]
	for _, summary := range summaries {
		if summary.ID != id {
			kept = append(kept, summary)
		}
	}
	return kept
}

func latestRefID(summaries []session.Ref) string {
	var id string
	var latest time.Time
	for _, summary := range summaries {
		if id == "" || summary.UpdatedAt.After(latest) {
			id = summary.ID
			latest = summary.UpdatedAt
		}
	}
	return id
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session dir %q: %w", filepath.Dir(path), err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write session file %q: %w", path, err)
	}
	return nil
}
