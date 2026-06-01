package fsutil_test

import (
	"testing"

	"github.com/crowl/ronin/tool/fsutil"
)

func TestReadCache(t *testing.T) {
	t.Run("returns stable entry for same path and sha", func(t *testing.T) {
		cache := fsutil.NewReadCache()

		first := cache.GetOrCreate("file.txt", "sha1")
		second := cache.GetOrCreate("file.txt", "sha1")

		if first != second {
			t.Fatalf("second entry = %#v, want %#v", second, first)
		}
		if first.ID == "" {
			t.Fatal("entry ID is empty")
		}
	})

	t.Run("uses separate entries for different path or sha", func(t *testing.T) {
		cache := fsutil.NewReadCache()

		first := cache.GetOrCreate("file.txt", "sha1")
		differentSHA := cache.GetOrCreate("file.txt", "sha2")
		differentPath := cache.GetOrCreate("other.txt", "sha1")

		if first.ID == differentSHA.ID {
			t.Fatalf("same ID for different SHA: %q", first.ID)
		}
		if first.ID == differentPath.ID {
			t.Fatalf("same ID for different path: %q", first.ID)
		}
	})

	t.Run("marks existing entry as full", func(t *testing.T) {
		cache := fsutil.NewReadCache()
		entry := cache.GetOrCreate("file.txt", "sha1")

		if cache.HasFull("file.txt", "sha1") {
			t.Fatal("HasFull() = true before MarkFull")
		}

		marked := cache.MarkFull("file.txt", "sha1")
		if marked.ID != entry.ID {
			t.Fatalf("MarkFull() ID = %q, want existing ID %q", marked.ID, entry.ID)
		}
		if !marked.FullReturned {
			t.Fatal("MarkFull() FullReturned = false, want true")
		}
		if !cache.HasFull("file.txt", "sha1") {
			t.Fatal("HasFull() = false after MarkFull")
		}
	})

	t.Run("mark full creates entry", func(t *testing.T) {
		cache := fsutil.NewReadCache()

		marked := cache.MarkFull("file.txt", "sha1")
		entry := cache.GetOrCreate("file.txt", "sha1")

		if marked != entry {
			t.Fatalf("GetOrCreate() = %#v, want marked entry %#v", entry, marked)
		}
		if !entry.FullReturned {
			t.Fatal("FullReturned = false, want true")
		}
	})

	t.Run("nil cache returns fallback entries", func(t *testing.T) {
		var cache *fsutil.ReadCache

		entry := cache.GetOrCreate("file.txt", "sha1")
		if entry.ID != "file_000000" || entry.Path != "file.txt" || entry.SHA256 != "sha1" || entry.FullReturned {
			t.Fatalf("GetOrCreate() = %#v, want fallback non-full entry", entry)
		}
		if cache.HasFull("file.txt", "sha1") {
			t.Fatal("HasFull() = true for nil cache, want false")
		}

		marked := cache.MarkFull("file.txt", "sha1")
		if marked.ID != "file_000000" || marked.Path != "file.txt" || marked.SHA256 != "sha1" || !marked.FullReturned {
			t.Fatalf("MarkFull() = %#v, want fallback full entry", marked)
		}
	})
}
