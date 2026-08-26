package gosemantic

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/crowl/ronin/tool"
)

// module is the result of loading the local Go module(s) in syntax-only mode.
// It is the single boundary that owns go/packages; the tools consume only the
// parsed syntax it exposes. References and implementations support will widen
// the load mode here without changing callers.
type module struct {
	fset     *token.FileSet
	packages []*packages.Package
	warnings []string
}

// loadMode is syntax-only: enough to locate and describe declarations.
// References and implementations support will add NeedTypes here.
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedSyntax

// loadModule loads every package in the local Go module(s) for workingDir.
// It searches upwards and downwards to locate all applicable module roots or workspaces.
// Per-package parse errors are reported as warnings rather than failing the
// whole load, because an active conversation often edits code that does not compile.
func loadModule(ctx context.Context, workingDir string) (*module, error) {
	if ctx.Err() != nil {
		return nil, tool.Error{Code: "package_load_failed", Message: ctx.Err().Error()}
	}

	roots, err := findModuleRoots(workingDir)
	if err != nil {
		return nil, tool.Error{Code: "package_load_failed", Message: err.Error()}
	}

	fset := token.NewFileSet()
	var allPkgs []*packages.Package
	var warnings []string
	seenPkgIDs := make(map[string]bool)

	for _, root := range roots {
		cfg := &packages.Config{
			Context: ctx,
			Dir:     root,
			Mode:    loadMode,
			Fset:    fset,
		}

		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			if ctx.Err() != nil {
				return nil, tool.Error{Code: "package_load_failed", Message: err.Error()}
			}
			warnings = append(warnings, fmt.Sprintf("%s: %v", root, err))
			continue
		}

		for _, pkg := range pkgs {
			id := pkg.ID
			if id == "" {
				id = pkg.PkgPath
			}
			if seenPkgIDs[id] {
				continue
			}
			seenPkgIDs[id] = true
			allPkgs = append(allPkgs, pkg)

			for _, e := range pkg.Errors {
				warnings = append(warnings, pkg.PkgPath+": "+e.Error())
			}
		}
	}

	if ctx.Err() != nil {
		return nil, tool.Error{Code: "package_load_failed", Message: ctx.Err().Error()}
	}

	if len(allPkgs) == 0 {
		return nil, tool.Error{Code: "package_load_failed", Message: "no packages found in module"}
	}

	return &module{
		fset:     fset,
		packages: allPkgs,
		warnings: warnings,
	}, nil
}

// findModuleRoots discovers all Go module roots associated with dir.
// It checks:
// 1. Upwards: dir or an ancestor containing go.work (workspace mode) or go.mod.
// 2. Downwards: subdirectories containing go.mod (monorepos, workspaces, or nested modules).
func findModuleRoots(dir string) ([]string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)

	var ancestorModRoot string
	var workspaceRoot string

	curr := abs
	for {
		if workspaceRoot == "" {
			if info, err := os.Stat(filepath.Join(curr, "go.work")); err == nil && !info.IsDir() {
				workspaceRoot = curr
			}
		}
		if ancestorModRoot == "" {
			if info, err := os.Stat(filepath.Join(curr, "go.mod")); err == nil && !info.IsDir() {
				ancestorModRoot = curr
			}
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	// When in a Go workspace, discover all modules under the workspace root.
	searchDownRoot := abs
	if workspaceRoot != "" {
		searchDownRoot = workspaceRoot
	}

	// Discover nested modules or submodules.
	var subModules []string
	_ = filepath.WalkDir(searchDownRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			name := d.Name()
			if path != searchDownRoot {
				if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() == "go.mod" {
			subModules = append(subModules, filepath.Dir(path))
		}
		return nil
	})

	var roots []string
	seen := make(map[string]bool)

	if ancestorModRoot != "" {
		roots = append(roots, ancestorModRoot)
		seen[ancestorModRoot] = true
	}

	for _, sub := range subModules {
		if !seen[sub] {
			roots = append(roots, sub)
			seen[sub] = true
		}
	}

	if len(roots) == 0 {
		return nil, errors.New("no go.mod found in working directory or any parent")
	}

	return roots, nil
}
