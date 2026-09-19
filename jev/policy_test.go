package jev

import "testing"

func TestDecide(t *testing.T) {
	const min = 0.55
	cases := []struct {
		name string
		j    Judgment
		want Action
	}{
		{
			name: "confident deny",
			j:    Judgment{Disposition: "deny", Confidence: 0.9, Irreversible: 0.2, Impact: 0.4},
			want: ActionDeny,
		},
		{
			name: "low-confidence deny is allowed",
			j:    Judgment{Disposition: "deny", Confidence: 0.4, Irreversible: 0.9, Impact: 2},
			want: ActionAllow,
		},
		{
			name: "irreversible without allow disposition",
			j:    Judgment{Disposition: "deny", Confidence: 0.7, Irreversible: 0.85, Impact: 0.2},
			want: ActionDeny,
		},
		{
			name: "allow even when irreversible if disposition is allow",
			j:    Judgment{Disposition: "allow", Confidence: 0.9, Irreversible: 0.95, Impact: 2},
			want: ActionAllow,
		},
		{
			name: "empty judgment allows",
			j:    Judgment{},
			want: ActionAllow,
		},
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
	j := judgmentFrom(map[string]Answer{
		"disposition":  {Type: "choice", Choice: "deny", Confidence: 0.81, Probabilities: map[string]float64{"deny": 0.8, "allow": 0.2}},
		"irreversible": {Type: "noul", Noul: 0.91},
		"impact":       {Type: "score", Score: 1.7},
	})
	if j.Disposition != "deny" || j.Confidence != 0.81 || j.Irreversible != 0.91 || j.Impact != 1.7 {
		t.Fatalf("judgment = %+v", j)
	}
	if Decide(j, 0.55) != ActionDeny {
		t.Fatal("expected deny")
	}
	if reason := denyReason(j); reason == "" {
		t.Fatal("expected deny reason")
	}
}
