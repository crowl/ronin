package runtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/runtime"
)

func TestLoadSkills(t *testing.T) {
	t.Run("missing directory returns no skills", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		skills, err := runtime.LoadSkills(dir)
		if err != nil {
			t.Fatalf("LoadSkills() error = %v", err)
		}
		if len(skills) != 0 {
			t.Fatalf("len(skills) = %d, want 0", len(skills))
		}
	})

	t.Run("skips directories without skill file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		skills, err := runtime.LoadSkills(dir)
		if err != nil {
			t.Fatalf("LoadSkills() error = %v", err)
		}
		if len(skills) != 0 {
			t.Fatalf("len(skills) = %d, want 0", len(skills))
		}
	})

	t.Run("invalid skill returns error", func(t *testing.T) {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, "bad")
		if err := os.Mkdir(skillDir, 0o755); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		writeFile(t, filepath.Join(skillDir, "SKILL.md"), "not frontmatter")
		_, err := runtime.LoadSkills(dir)
		if err == nil || !strings.Contains(err.Error(), "failed to parse") {
			t.Fatalf("LoadSkills() error = %v, want parse error", err)
		}
	})

	t.Run("duplicate skill returns error", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, filepath.Join(dir, "one"), "one", "first")
		writeSkill(t, filepath.Join(dir, "two"), "one", "second")
		_, err := runtime.LoadSkills(dir)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("LoadSkills() error = %v, want duplicate error", err)
		}
	})
}
