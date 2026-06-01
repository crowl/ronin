package fsutil

import (
	"fmt"
	"sync"
)

type ReadEntry struct {
	ID           string
	Path         string
	SHA256       string
	FullReturned bool
}

type ReadCache struct {
	mu      sync.Mutex
	nextID  uint64
	entries map[string]ReadEntry
}

func NewReadCache() *ReadCache {
	return &ReadCache{entries: make(map[string]ReadEntry)}
}

func (c *ReadCache) GetOrCreate(path string, sha256 string) ReadEntry {
	if c == nil {
		return ReadEntry{ID: "file_000000", Path: path, SHA256: sha256}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]ReadEntry)
	}

	key := readEntryKey(path, sha256)
	if entry, ok := c.entries[key]; ok {
		return entry
	}

	c.nextID++
	entry := ReadEntry{
		ID:     fmt.Sprintf("file_%06d", c.nextID),
		Path:   path,
		SHA256: sha256,
	}
	c.entries[key] = entry
	return entry
}

func (c *ReadCache) HasFull(path string, sha256 string) bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.entries[readEntryKey(path, sha256)].FullReturned
}

func (c *ReadCache) MarkFull(path string, sha256 string) ReadEntry {
	if c == nil {
		return ReadEntry{ID: "file_000000", Path: path, SHA256: sha256, FullReturned: true}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]ReadEntry)
	}

	key := readEntryKey(path, sha256)
	entry, ok := c.entries[key]
	if !ok {
		c.nextID++
		entry = ReadEntry{
			ID:     fmt.Sprintf("file_%06d", c.nextID),
			Path:   path,
			SHA256: sha256,
		}
	}
	entry.FullReturned = true
	c.entries[key] = entry
	return entry
}

func readEntryKey(path string, sha256 string) string {
	return path + "\x00" + sha256
}
