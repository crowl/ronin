package guard

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/plugin"
)

func TestVerdict(t *testing.T) {
	cases := []struct {
		name    string
		shell   bool
		answers map[string]plugin.Answer
		deny    bool
	}{
		{name: "on task", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.9}}},
		{name: "uncertain relevance", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.5}}},
		{name: "off task", answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.1}}, deny: true},
		{name: "shell allowed", shell: true, answers: map[string]plugin.Answer{
			"relevant": {Type: "noul", Noul: 0.9}, "file_bypass": {Type: "noul", Noul: 0.2},
		}},
		{name: "shell bypass", shell: true, answers: map[string]plugin.Answer{
			"relevant": {Type: "noul", Noul: 0.9}, "file_bypass": {Type: "noul", Noul: 0.8},
		}, deny: true},
		{name: "missing bypass answer", shell: true, answers: map[string]plugin.Answer{
			"relevant": {Type: "noul", Noul: 0.9},
		}, deny: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verdict(tc.answers, tc.shell)
			if tc.deny && err == nil {
				t.Fatal("expected denial")
			}
			if !tc.deny && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOffTaskReasonOmitsBypass(t *testing.T) {
	err := verdict(map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.1}}, false)
	if err == nil || !strings.Contains(err.Error(), "relevant=0.10") || strings.Contains(err.Error(), "file_bypass") {
		t.Fatalf("reason = %v", err)
	}
}
