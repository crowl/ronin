package codeindex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodedBudget(t *testing.T) {
	r := Result{Entries: []Entry{}}
	for range 100 {
		r.Entries = append(r.Entries, Entry{Path: strings.Repeat("<", 512), Name: strings.Repeat("<", 160), Signature: strings.Repeat("<", 320)})
	}
	boundJSON(&r)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 64*1024 || !r.Truncated || len(r.Entries) == 0 {
		t.Fatalf("budget: bytes=%d entries=%d truncated=%v", len(data), len(r.Entries), r.Truncated)
	}
}

func TestSourceMatchesBoundedPerFile(t *testing.T) {
	root := t.TempDir()
	var noisy strings.Builder
	noisy.WriteString("package p\n")
	for range 12 {
		noisy.WriteString("// marker line\n")
	}
	write(t, root, "noisy.go", noisy.String())
	write(t, root, "quiet.go", "package p\n// marker once\n")
	result, err := New(root, "").Find(t.Context(), Query{Text: "marker"})
	if err != nil {
		t.Fatal(err)
	}
	perFile := map[string]int{}
	omitted := 0
	for _, e := range result.Entries {
		if e.Match != "source" {
			t.Fatalf("unexpected entry: %+v", e)
		}
		perFile[e.Path]++
		omitted += e.OmittedLines
	}
	if perFile["noisy.go"] != maxSourceMatchesPerFile || perFile["quiet.go"] != 1 || omitted != 12-maxSourceMatchesPerFile || result.Truncated {
		t.Fatalf("per-file bound: files=%v omitted=%d truncated=%v", perFile, omitted, result.Truncated)
	}
}

func write(t *testing.T, root, name, source string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWordMatching(t *testing.T) {
	for _, tt := range []struct {
		query, text string
		want        bool
	}{{"validate session", "ValidateSession", true}, {"session expiry", "session_expiry", true}, {"session expiry", "session only", false}} {
		if got := allTerms(words(tt.query), tt.text); got != tt.want {
			t.Errorf("%q in %q: %v", tt.query, tt.text, got)
		}
	}
}
