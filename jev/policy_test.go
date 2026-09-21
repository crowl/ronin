package jev

import "testing"

func TestDecide(t *testing.T) {
	const min = 0.55
	cases := []struct {
		name string
		j    Judgment
		want Action
	}{
		{name: "off-task is denied", j: Judgment{Relevant: 0.12}, want: ActionDeny},
		{name: "zero relevance is denied", j: Judgment{Relevant: 0}, want: ActionDeny},
		{name: "uncertain relevance is allowed", j: Judgment{Relevant: 0.5}, want: ActionAllow},
		{name: "on-task is allowed", j: Judgment{Relevant: 0.91}, want: ActionAllow},
		{name: "on-task irreversible still denied", j: Judgment{Relevant: 0.92, Irreversible: 0.96}, want: ActionDeny},
		{name: "weak irreversible is allowed when relevant", j: Judgment{Relevant: 0.8, Irreversible: 0.4}, want: ActionAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.j, min); got != tc.want {
				t.Fatalf("Decide() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestJudgmentFromAnswers(t *testing.T) {
	j, err := judgmentFrom(map[string]Answer{
		"relevant":     {Type: "noul", Noul: 0.11},
		"irreversible": {Type: "noul", Noul: 0.2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.Relevant != 0.11 || j.Irreversible != 0.2 {
		t.Fatalf("judgment = %+v", j)
	}
	if Decide(j, 0.55) != ActionDeny {
		t.Fatal("expected deny")
	}
	if reason := denyReason(j); reason == "" || reason[:3] != "Jev" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestJudgmentRejectsMalformedAnswers(t *testing.T) {
	cases := []map[string]Answer{
		nil,
		{"relevant": {Type: "noul", Noul: 0.5}},
		{"relevant": {Type: "choice"}, "irreversible": {Type: "noul"}},
		{"relevant": {Type: "noul", Noul: 1.1}, "irreversible": {Type: "noul"}},
	}
	for _, answers := range cases {
		if _, err := judgmentFrom(answers); err == nil {
			t.Fatalf("judgmentFrom(%#v) error = nil", answers)
		}
	}
}
