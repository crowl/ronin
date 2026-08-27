package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const MaxContextFileBytes = 128 * 1024

const contextFileName = "AGENTS.md"

type ContextFile struct {
	Path    string
	Content string
}

func LoadContextFiles(configDir, cwd string) ([]ContextFile, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve cwd: %w", err)
	}

	var paths []string
	if configDir != "" {
		global := filepath.Join(configDir, contextFileName)
		if info, err := os.Stat(global); err == nil && !info.IsDir() {
			paths = append(paths, global)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat context file %q: %w", global, err)
		}
	}

	var localPaths []string
	current := filepath.Clean(abs)
	for {
		candidate := filepath.Join(current, contextFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			localPaths = append(localPaths, candidate)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat context file %q: %w", candidate, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	for _, localPath := range slices.Backward(localPaths) {
		paths = append(paths, localPath)
	}

	var files []ContextFile
	seen := map[string]bool{}

	for _, path := range paths {
		clean, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve context file %q: %w", path, err)
		}
		clean = filepath.Clean(clean)
		if seen[clean] {
			continue
		}
		seen[clean] = true

		info, err := os.Stat(clean)
		if err != nil {
			return nil, fmt.Errorf("stat context file %q: %w", clean, err)
		}
		if info.Size() > MaxContextFileBytes {
			return nil, fmt.Errorf("context file %q exceeds %d bytes", clean, MaxContextFileBytes)
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return nil, fmt.Errorf("read context file %q: %w", clean, err)
		}
		files = append(files, ContextFile{Path: clean, Content: string(data)})
	}

	return files, nil
}
