package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	basefsutil "github.com/crowl/ronin/fsutil"
)

type ResolvedPath struct {
	Display string
	Abs     string
}

func ResolvePath(cwd string, path string) (ResolvedPath, error) {
	return resolvePath(cwd, path, true, false)
}

func ResolvePathWithinCWD(cwd string, path string) (ResolvedPath, error) {
	return resolvePath(cwd, path, true, true)
}

func ResolvePathForWrite(cwd string, path string) (ResolvedPath, error) {
	return resolvePath(cwd, path, false, false)
}

func ResolvePathForWriteWithinCWD(cwd string, path string) (ResolvedPath, error) {
	return resolvePath(cwd, path, false, true)
}

func resolvePath(cwd string, path string, followLeaf, requireWithinCWD bool) (ResolvedPath, error) {
	if cwd == "" {
		cwd = "."
	}

	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return ResolvedPath{}, err
	}
	if realCWD, err := filepath.EvalSymlinks(absCWD); err == nil {
		absCWD = realCWD
	}

	if path == "" {
		path = "."
	}

	clean := filepath.Clean(path)
	abs := clean
	if !filepath.IsAbs(clean) {
		abs = filepath.Join(absCWD, clean)
	}

	var resolvedAbs string
	if followLeaf {
		resolvedAbs, err = ResolveExistingOrParent(abs)
	} else {
		resolvedAbs, err = resolveParent(abs)
	}
	if err != nil {
		return ResolvedPath{}, err
	}
	if requireWithinCWD && !pathWithin(absCWD, resolvedAbs) {
		return ResolvedPath{}, errors.New("path resolves outside the working directory")
	}

	return ResolvedPath{
		Display: DisplayPath(absCWD, resolvedAbs),
		Abs:     resolvedAbs,
	}, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ResolveExistingOrParent(abs string) (string, error) {
	if realPath, err := filepath.EvalSymlinks(abs); err == nil {
		return realPath, nil
	}

	dir := filepath.Dir(abs)
	suffix := filepath.Base(abs)

	for {
		if realDir, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(realDir, suffix), nil
		}

		next := filepath.Dir(dir)
		if next == dir {
			return "", errors.New("could not resolve path parent")
		}
		suffix = filepath.Join(filepath.Base(dir), suffix)
		dir = next
	}
}

func resolveParent(abs string) (string, error) {
	dir, err := ResolveExistingOrParent(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

func DisplayPath(cwd string, abs string) string {
	rel, err := filepath.Rel(cwd, abs)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
		return filepath.ToSlash(rel)
	}

	return filepath.ToSlash(abs)
}

func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	return basefsutil.WriteFileAtomic(path, data, mode)
}
