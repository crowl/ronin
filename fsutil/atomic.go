package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const tmpFilePattern = ".ronin-*"

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
