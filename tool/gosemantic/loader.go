package gosemantic

import (
	"context"
	"errors"
	"go/token"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/crowl/ronin/tool"
)

// module is the result of loading the local Go module in syntax-only mode.
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

// loadModule loads every package in the local module containing workingDir.
// Per-package parse errors are reported as warnings rather than failing the
// whole load, because an agent session often edits code that does not compile.
func loadModule(ctx context.Context, workingDir string) (*module, error) {
	root, err := findModuleRoot(workingDir)
	if err != nil {
		return nil, tool.Error{Code: "package_load_failed", Message: err.Error()}
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: ctx,
		Dir:     root,
		Mode:    loadMode,
		Fset:    fset,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, tool.Error{Code: "package_load_failed", Message: err.Error()}
	}
	if len(pkgs) == 0 {
		return nil, tool.Error{Code: "package_load_failed", Message: "no packages found in module"}
	}

	var warnings []string
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			warnings = append(warnings, pkg.PkgPath+": "+e.Error())
		}
	}

	return &module{
		fset:     fset,
		packages: pkgs,
		warnings: warnings,
	}, nil
}

func findModuleRoot(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		info, err := os.Stat(filepath.Join(abs, "go.mod"))
		if err == nil && !info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", errors.New("no go.mod found in working directory or any parent")
		}
		abs = parent
	}
}
