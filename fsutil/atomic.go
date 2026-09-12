package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const tmpFilePattern = ".ronin-*"

// WriteFileAtomic replaces path with data so readers observe either the old
// or the new content, never a partial write. The replacement is a new inode
// renamed over the destination: hard links to the previous file are detached,
// the file is owned by the current user, and inotify-style watchers see a
// delete followed by a create rather than a modification. Only mode's
// permission bits are applied.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, tmpFilePattern)
	if err != nil {
		return fmt.Errorf("write file %q: create temporary file: %w", path, err)
	}

	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write file %q: write temporary file: %w", path, err)
	}

	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write file %q: chmod temporary file: %w", path, err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write file %q: sync temporary file: %w", path, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write file %q: close temporary file: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write file %q: rename temporary file: %w", path, err)
	}

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("write file %q: sync parent directory after destination was replaced: %w", path, err)
	}
	return nil
}
