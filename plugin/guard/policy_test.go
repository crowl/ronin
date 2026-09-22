package guard

import (
	"math"
	"strings"
	"testing"

	"github.com/crowl/ronin/plugin"
)

func TestVerdict(t *testing.T) {
	cases := []struct {
		name    string
		answers map[string]plugin.Answer
		deny    bool
	}{
		{name: "on task", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.9}}},
		{name: "uncertain relevance", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.5}}},
		{name: "off task", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.1}}, deny: true},
		{name: "missing answer", answers: map[string]plugin.Answer{}},
		{name: "nil answers", answers: nil},
		{name: "wrong type", answers: map[string]plugin.Answer{"relevant": {Type: "choice", Noul: 0.1}}},
		{name: "invalid answer", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 1.1}}},
		{name: "negative answer", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: -0.1}}},
		{name: "not a number", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: math.NaN()}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verdict(tc.answers)
			if tc.deny && err == nil {
				t.Fatal("expected denial")
			}
			if !tc.deny && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOffTaskReason(t *testing.T) {
	err := verdict(map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.1}})
	if err == nil || !strings.Contains(err.Error(), "relevant=0.10") {
		t.Fatalf("reason = %v", err)
	}
}
