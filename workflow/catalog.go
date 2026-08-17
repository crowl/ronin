package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	lua "github.com/Shopify/go-lua"
)

// Workflow identifies a named Lua workflow loaded at startup.
type Workflow struct {
	Name string
	Path string
}

// Catalog is an immutable collection of named workflows.
type Catalog struct {
	workflows []Workflow
	byName    map[string]Workflow
}

// LoadCatalog loads flat .lua workflow files from dir.
func LoadCatalog(dir string) (*Catalog, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("workflow directory is required")
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve workflow directory %q: %w", dir, err)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return newCatalog(nil), nil
		}
		return nil, fmt.Errorf("read workflow directory %q: %w", dir, err)
	}

	workflows := make([]Workflow, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".lua" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".lua")
		if !validWorkflowName(name) {
			return nil, fmt.Errorf("invalid workflow filename %q", entry.Name())
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate workflow name %q", name)
		}

		path := filepath.Join(absDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat workflow %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("workflow %q is not a regular file", path)
		}
		if err := validateFile(path); err != nil {
			return nil, err
		}
		seen[name] = struct{}{}
		workflows = append(workflows, Workflow{Name: name, Path: path})
	}

	sort.Slice(workflows, func(i, j int) bool { return workflows[i].Name < workflows[j].Name })
	return newCatalog(workflows), nil
}

func newCatalog(workflows []Workflow) *Catalog {
	copied := append([]Workflow(nil), workflows...)
	byName := make(map[string]Workflow, len(copied))
	for _, item := range copied {
		byName[item.Name] = item
	}
	return &Catalog{workflows: copied, byName: byName}
}

// Workflows returns the catalog entries in stable name order.
func (c *Catalog) Workflows() []Workflow {
	if c == nil {
		return nil
	}
	return append([]Workflow(nil), c.workflows...)
}

// Lookup returns the named workflow.
func (c *Catalog) Lookup(name string) (Workflow, bool) {
	if c == nil {
		return Workflow{}, false
	}
	item, ok := c.byName[name]
	return item, ok
}

func validateFile(path string) error {
	script, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read workflow %q: %w", path, err)
	}
	state := lua.NewState()
	if err := lua.LoadBuffer(state, string(script), path, ""); err != nil {
		return fmt.Errorf("parse workflow %q: %w", path, err)
	}
	return nil
}

func validWorkflowName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}
