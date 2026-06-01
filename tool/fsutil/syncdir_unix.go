//go:build unix || darwin || linux

package fsutil

import (
	"errors"
	"os"
	"syscall"
)

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	if err := file.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EBADF) {
			return nil
		}
		return err
	}

	return nil
}
