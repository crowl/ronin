package codeindex_test

import (
	"os"
	"testing"
	"time"

	"github.com/crowl/ronin/codeindex"
)

// Opt-in because local repositories are not hermetic CI fixtures. Writes only
// to t.TempDir, never into the supplied project.
func TestProjectSmoke(t *testing.T) {
	root := os.Getenv("RONIN_INDEX_TEST_ROOT")
	if root == "" {
		t.Skip("set RONIN_INDEX_TEST_ROOT for a local-project scan")
	}
	index := codeindex.New(root, t.TempDir())
	start := time.Now()
	first, err := index.Map(t.Context(), codeindex.Query{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cold map: %s, files=%d parsed=%d issues=%d", time.Since(start), first.Status.Files, first.Status.Parsed, first.Status.Issues)
	start = time.Now()
	second, err := index.Find(t.Context(), codeindex.Query{Text: "function", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("warm find: %s, files=%d reused=%d results=%d", time.Since(start), second.Status.Files, second.Status.Reused, len(second.Entries))
	if first.Status.Files == 0 || second.Status.Files != first.Status.Files {
		t.Fatal("unexpected corpus counts")
	}
}
