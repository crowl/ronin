package codeindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"time"
	"unicode/utf8"

	"github.com/crowl/ronin/codeindex/syntax"
)

const maxFileBytes = 1024 * 1024
const maxWorkspaceBytes = 64 * 1024 * 1024
const maxFiles = 10000

func scan(ctx context.Context, root *os.Root, old map[string]fileRecord) ([]fileRecord, Status, error) {
	var records []fileRecord
	var status Status
	var total, structure int
	engines := map[string]*syntax.Extractor{}
	defer func() {
		for _, e := range engines {
			e.Close()
		}
	}()
	var walk func(string, ignoreRules) error
	walk = func(dir string, inherited ignoreRules) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		patterns := inherited
		ignorePath := path.Join(dir, ".gitignore")
		if info, err := root.Lstat(ignorePath); err == nil && info.Mode().IsRegular() {
			data, err := readBounded(root, ignorePath, 64*1024)
			if err != nil {
				return fmt.Errorf("read ignore rules %s: %w", ignorePath, err)
			}
			patterns = inherited.appendFile(dir, data)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		entries, err := fs.ReadDir(root.FS(), dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := path.Join(dir, entry.Name())
			if len(name) > 512 {
				status.Skipped++
				status.notice("", "path longer than 512 bytes skipped")
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "vendor", "dist", "build", ".next", ".nuxt", ".output", "coverage":
					continue
				}
			}
			if patterns.ignored(name, entry.IsDir()) {
				continue
			}
			if entry.IsDir() {
				if err := walk(name, patterns); err != nil {
					return err
				}
				continue
			}
			language := syntax.Language(name)
			if language == "" || !entry.Type().IsRegular() {
				continue
			}
			if len(records) >= maxFiles {
				return fmt.Errorf("workspace exceeds %d supported files; narrow working directory", maxFiles)
			}
			data, err := readBounded(root, name, maxFileBytes)
			if err != nil {
				status.Skipped++
				status.notice(name, err.Error())
				continue
			}
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				status.Skipped++
				status.notice(name, "non-UTF-8 or binary source skipped")
				continue
			}
			total += len(data)
			if total > maxWorkspaceBytes {
				return fmt.Errorf("workspace exceeds 64 MiB source limit; narrow working directory")
			}
			sha := fmt.Sprintf("%x", sha256.Sum256(data))
			r, ok := old[name]
			if ok && r.SHA256 == sha && r.Issue == "" {
				status.Reused++
			} else {
				r = fileRecord{Path: name, Language: language, SHA256: sha, Source: string(data)}
				e := engines[language]
				if e == nil {
					e, err = syntax.New(language)
					if err != nil {
						return err
					}
					engines[language] = e
				}
				parseCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				r.Outline, err = e.Extract(parseCtx, data)
				cancel()
				status.Parsed++
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					r.Issue = "structural extraction failed: " + err.Error()
				}
			}
			for _, d := range r.Outline.Declarations {
				structure += len(d.Name) + len(d.Container) + len(d.Signature) + 128
			}
			if total+structure > 128*1024*1024 {
				return fmt.Errorf("workspace exceeds 128 MiB source/structure budget; narrow working directory")
			}
			if r.Issue != "" {
				status.notice(name, r.Issue)
			} else if len(r.Outline.Diagnostics) > 0 {
				status.notice(name, "parser diagnostics; structure may be incomplete (not compiler errors)")
			}
			records = append(records, r)
		}
		return nil
	}
	err := walk(".", nil)
	status.Files = len(records)
	return records, status, err
}

func readBounded(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if before.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit", limit)
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("file changed while reading; retry")
	}
	return data, nil
}
