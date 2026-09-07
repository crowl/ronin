package codeindex

import (
	"encoding/json"
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
