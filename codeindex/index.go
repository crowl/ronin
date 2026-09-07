// Package codeindex provides workspace-scoped, disposable source navigation.
package codeindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crowl/ronin/codeindex/syntax"
	_ "modernc.org/sqlite"
)

// Bump when schema, extraction rules, or pinned grammars change. Cache identity
// includes the canonical workspace, not a branch or shared Git directory.
const cacheVersion = "v2-go025-ts92634ed-binding8486ff"

// Index is immutable and safe for concurrent calls. Each operation opens and
// closes its own database/root/parser resources; no background workers survive it.
type Index struct{ workspace, cacheDir string }

func New(workspace, cacheDir string) *Index { return &Index{workspace: workspace, cacheDir: cacheDir} }

type fileRecord struct {
	Path     string
	Language string
	SHA256   string
	Source   string
	Outline  syntax.Outline
	Issue    string
}

type Notice struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}
type Status struct {
	Files     int      `json:"files"`
	Parsed    int      `json:"parsed"`
	Reused    int      `json:"reused"`
	Skipped   int      `json:"skipped"`
	Issues    int      `json:"issues"`
	Notices   []Notice `json:"notices,omitempty"`
	CheckedAt string   `json:"checked_at"`
}

func (s *Status) notice(path, message string) {
	s.Issues++
	if len(s.Notices) < 10 {
		s.Notices = append(s.Notices, Notice{path, clip(message, 200)})
	}
}

// snapshot refreshes within a write transaction so competing sessions cannot
// publish an older scan after a newer one. Failures/cancellation roll back; a
// failed call never returns the previous cache as if it were current.
func (i *Index) snapshot(ctx context.Context) (records []fileRecord, status Status, err error) {
	if err := ctx.Err(); err != nil {
		return nil, status, err
	}
	workspace, err := filepath.Abs(i.workspace)
	if err != nil {
		return nil, status, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, status, err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, status, err
	}
	defer root.Close()
	cache := i.cacheDir
	if cache == "" {
		cache, err = os.UserCacheDir()
		if err != nil {
			return nil, status, err
		}
		cache = filepath.Join(cache, "ronin", "codeindex")
	}
	cache, err = filepath.Abs(cache)
	if err != nil {
		return nil, status, err
	}
	// Resolve existing parents before deciding whether the cache is outside root.
	resolvedCache, err := resolveCache(cache)
	if err != nil {
		return nil, status, err
	}
	rel, err := filepath.Rel(workspace, resolvedCache)
	if filepath.VolumeName(workspace) == filepath.VolumeName(resolvedCache) && (rel == "." || (err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return nil, status, errors.New("index cache must be outside workspace")
	}
	if err = os.MkdirAll(resolvedCache, 0700); err != nil {
		return nil, status, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace+"\x00"+cacheVersion)))
	dbPath := filepath.Join(resolvedCache, key+".db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String()+"?_pragma=busy_timeout(1000)&_txlock=immediate")
	if err != nil {
		return nil, status, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS files(path TEXT PRIMARY KEY, sha TEXT NOT NULL, language TEXT NOT NULL, source TEXT NOT NULL, outline TEXT NOT NULL, issue TEXT NOT NULL)`); err != nil {
		return nil, status, fmt.Errorf("open index cache (remove %s to rebuild): %w", dbPath, err)
	}
	if err = os.Chmod(dbPath, 0600); err != nil {
		return nil, status, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status, err
	}
	defer tx.Rollback()
	old := map[string]fileRecord{}
	rows, err := tx.QueryContext(ctx, "SELECT path,sha,language,source,outline,issue FROM files")
	if err != nil {
		return nil, status, err
	}
	var cachedBytes int
	for rows.Next() {
		var r fileRecord
		var raw string
		if err = rows.Scan(&r.Path, &r.SHA256, &r.Language, &r.Source, &raw, &r.Issue); err != nil {
			rows.Close()
			return nil, status, err
		}
		cachedBytes += len(r.Source) + len(raw)
		if cachedBytes > 256*1024*1024 || len(old) >= maxFiles {
			rows.Close()
			return nil, status, fmt.Errorf("index cache exceeds limits; remove %s to rebuild", dbPath)
		}
		if json.Unmarshal([]byte(raw), &r.Outline) == nil {
			old[r.Path] = r
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, status, err
	}
	records, status, err = scan(ctx, root, old)
	if err != nil {
		return nil, status, err
	}
	for _, r := range records {
		previous, ok := old[r.Path]
		if ok && previous.SHA256 == r.SHA256 && previous.Issue == "" {
			continue
		}
		raw, marshalErr := json.Marshal(r.Outline)
		if marshalErr != nil {
			return nil, status, marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO files VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET sha=excluded.sha,language=excluded.language,source=excluded.source,outline=excluded.outline,issue=excluded.issue`, r.Path, r.SHA256, r.Language, r.Source, string(raw), r.Issue)
		if err != nil {
			return nil, status, err
		}
	}
	// Include malformed cached rows in deletion, not just successfully decoded ones.
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE seen(path TEXT PRIMARY KEY)"); err != nil {
		return nil, status, err
	}
	for _, r := range records {
		if _, err = tx.ExecContext(ctx, "INSERT INTO seen VALUES(?)", r.Path); err != nil {
			return nil, status, err
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM files WHERE path NOT IN (SELECT path FROM seen)"); err != nil {
		return nil, status, err
	}
	if err = tx.Commit(); err != nil {
		return nil, status, err
	}
	status.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return records, status, nil
}

func resolveCache(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	real, err = resolveCache(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(path)), nil
}
