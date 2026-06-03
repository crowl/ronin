package agent

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Path        string
}

func (s Skill) XML() string {
	var b strings.Builder
	b.WriteString("<skill>\n")
	b.WriteString("  <name>" + html.EscapeString(s.Name) + "</name>\n")
	b.WriteString("  <description>" + html.EscapeString(s.Description) + "</description>\n")
	b.WriteString("  <path>" + html.EscapeString(s.Path) + "</path>\n")
	b.WriteString("</skill>")
	return b.String()
}

func LoadSkills(dir string) ([]Skill, error) {
	if dir == "" {
		return nil, fmt.Errorf("dir is required")
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat dir %q: %w", dir, err)
	}
	absDir, err = filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, err
	}

	var skills []Skill
	seen := map[string]Skill{}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read dir %q: %w", dir, err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillFile := filepath.Join(absDir, entry.Name(), "SKILL.md")

		data, err := os.ReadFile(skillFile)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read file %q: %w", skillFile, err)
		}

		skill, err := parseSkillFile(skillFile, string(data))
		if err != nil {
			return nil, fmt.Errorf("failed to parse skill file %q: %w", skillFile, err)
		}

		if _, exists := seen[skill.Name]; exists {
			return nil, fmt.Errorf("duplicate skill name %q", skill.Name)
		}
		seen[skill.Name] = skill

		skills = append(skills, skill)
	}

	return skills, nil
}

func parseSkillFile(path string, content string) (Skill, error) {
	frontmatter, err := parseFrontmatter(content)
	if err != nil {
		return Skill{}, err
	}

	name := strings.TrimSpace(frontmatter["name"])
	if !validSkillName(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}

	description := strings.TrimSpace(frontmatter["description"])
	if description == "" {
		return Skill{}, fmt.Errorf("skill description is required")
	}
	if len(description) > 1024 {
		return Skill{}, fmt.Errorf("skill description exceeds 1024 characters")
	}

	parentName := filepath.Base(filepath.Dir(path))
	if parentName != name {
		return Skill{}, fmt.Errorf("failed to parse skill name and parent directory name as %q: %q", parentName, name)
	}

	return Skill{
		Name:        name,
		Description: description,
		Path:        path,
	}, nil
}

func parseFrontmatter(content string) (map[string]string, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return nil, fmt.Errorf("skill file must start with frontmatter")
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	values := map[string]string{}
	closed := false
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	if !closed {
		return nil, fmt.Errorf("skill frontmatter is not closed")
	}
	return values, nil
}

func validSkillName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
