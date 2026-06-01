package diff_test

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/diff"
)

func TestDiff(t *testing.T) {
	t.Run("identical returns nil", func(t *testing.T) {
		got := diff.Diff("old", []byte("same\n"), "new", []byte("same\n"))
		if got != nil {
			t.Fatalf("Diff() = %q, want nil", got)
		}
	})

	t.Run("changed text returns unified diff", func(t *testing.T) {
		got := string(diff.Diff("old.txt", []byte("hello world\n"), "new.txt", []byte("hello gopher\n")))
		for _, want := range []string{"diff old.txt new.txt", "--- old.txt", "+++ new.txt", "@@ -1,1 +1,1 @@", "-hello world", "+hello gopher"} {
			if !strings.Contains(got, want) {
				t.Fatalf("Diff() missing %q in:\n%s", want, got)
			}
		}
	})
}
