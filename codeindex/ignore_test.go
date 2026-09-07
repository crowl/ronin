package codeindex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIgnoreRules(t *testing.T) {
	for _, tt := range []struct {
		name, base, rules, path string
		directory, want         bool
	}{
		{"comment", ".", "# a.go\n\na.go\n", "a.go", false, true},
		{"literal hash", ".", "\\#a.go\n", "#a.go", false, true},
		{"literal bang", ".", "\\!a.go\n", "!a.go", false, true},
		{"negation", ".", "*.go\n!a.go\n", "a.go", false, false},
		{"last wins", ".", "!a.go\n*.go\n", "a.go", false, true},
		{"leading space", ".", " a.go\n", " a.go", false, true},
		{"trim spaces", ".", "a.go   \n", "a.go", false, true},
		{"escaped space", ".", "a.go\\ \n", "a.go ", false, true},
		{"CRLF BOM", ".", "\xef\xbb\xbf*.go\r\n", "a.go", false, true},
		{"basename", ".", "*.go\n", "sub/a.go", false, true},
		{"anchored", ".", "/a.go\n", "sub/a.go", false, false},
		{"nested anchor", "sub", "/a.go\n", "sub/a.go", false, true},
		{"nested scope", "sub", "*.go\n", "sibling/a.go", false, false},
		{"slash anchor", ".", "a/b.go\n", "sub/a/b.go", false, false},
		{"directory", ".", "cache/\n", "sub/cache", true, true},
		{"not directory", ".", "cache/\n", "cache", false, false},
		{"star no slash", ".", "a/*.go\n", "a/b/c.go", false, false},
		{"double star zero", ".", "a/**/b.go\n", "a/b.go", false, true},
		{"double star many", ".", "a/**/b.go\n", "a/x/y/b.go", false, true},
		{"double star boundary", ".", "a/**/b.go\n", "a/xb.go", false, false},
		{"trailing double star", ".", "a/**\n", "a/x/y.go", false, true},
		{"ordinary multiple stars", ".", "a**b.go\n", "axxb.go", false, true},
		{"question byte", ".", "?.go\n", "é.go", false, false},
		{"two bytes", ".", "??.go\n", "é.go", false, true},
		{"range", ".", "[a-c].go\n", "b.go", false, true},
		{"negative range", ".", "[!a-c].go\n", "b.go", false, false},
		{"caret range", ".", "[^a-c].go\n", "z.go", false, true},
		{"literal bracket", ".", "[]a].go\n", "].go", false, true},
		{"POSIX", ".", "[[:digit:]].go\n", "1.go", false, true},
		{"class slash", ".", "a[!x]b.go\n", "a/b.go", false, false},
		{"invalid class", ".", "[abc\n", "[abc", false, false},
		{"trailing escape", ".", "a.go\\\n", "a.go", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rules := ignoreRules(nil).appendFile(tt.base, []byte(tt.rules))
			if got := rules.ignored(tt.path, tt.directory); got != tt.want {
				t.Fatalf("ignored(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIgnoreRuleInheritance(t *testing.T) {
	parent := ignoreRules(nil).appendFile(".", []byte("*.go\n"))
	child := parent.appendFile("sub", []byte("!keep.go\n"))
	sibling := parent.appendFile("other", []byte("!other.go\n"))
	if child.ignored("sub/keep.go", false) || !parent.ignored("sub/keep.go", false) || !sibling.ignored("sub/keep.go", false) {
		t.Fatal("child rules mutated/leaked into parent or sibling")
	}
	if !child.ignored("sub/drop.go", false) {
		t.Fatal("ancestor pattern lost")
	}
}

// Git is an optional test oracle, never a runtime dependency. Nonexistent paths
// are deliberate: --no-index tests matching, independently of tracked state.
func TestIgnoreAgainstGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is needed only for differential tests")
	}
	root := t.TempDir()
	home := t.TempDir()
	var env []string
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || upper == "HOME" || upper == "XDG_CONFIG_HOME" || upper == "LC_ALL" {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, "absent"), "LC_ALL=C")
	run := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), git, args...)
		cmd.Dir = root
		cmd.Env = env
		return cmd
	}
	if out, err := run("init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// Query real directories without a trailing slash, as WalkDir does. Git
	// treats a synthetic trailing slash as an extra empty path component.
	if err := os.MkdirAll(filepath.Join(root, "a", "x"), 0700); err != nil {
		t.Fatal(err)
	}
	patterns := []string{"*.go", "/a.go", "a/*.go", "a/**/b.go", "**/a.go", "a/**", "**", "a**b.go", "a/***/b.go", "a/**b.go", "a/*/b.go", "a/", "a\n!a/keep.go", "a/*\n!a/keep.go", "*.go\n!a.go\na.go", "[a-c].go", "[!a-c].go", "[^a-c].go", "[]a].go", "[-a].go", "[a-].go", "[a\\-c].go", "[[:alpha:]].go", "[[:digit:]].go", "[[:bogus:]].go", "[abc", "?.go", "??.go", "\\#a.go", "\\!a.go", "a.go  ", "a.go\\ ", "a.go\\  ", "a.go\\\\ ", "\\*.go", "a.go\\", "\xef\xbb\xbf*.go\r", "#a.go", " a.go", "!", "/", "a/**/", "a/**/**/b.go", "a[!x]b.go"}
	paths := []string{"a.go", "b.go", "c.go", "z.go", "1.go", "é.go", "].go", "-.go", "*.go", "#a.go", "!a.go", " a.go", "a.go ", "a.go\\", "a", "a/b.go", "a/keep.go", "a/x/b.go", "a/x/y/b.go", "a/xb.go", "a/x", "other/a.go", "other/a/b.go", "axxb.go", "a/x/yb.go", ".hidden.go", "a/\n/b.go"}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(pattern+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := run("-c", "core.ignoreCase=false", "check-ignore", "--no-index", "-z", "--stdin")
			cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
			output, err := cmd.Output()
			var exit *exec.ExitError
			if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
				t.Fatalf("git check-ignore: %v", err)
			}
			ignored := map[string]bool{}
			for _, name := range strings.Split(string(output), "\x00") {
				ignored[name] = true
			}
			rules := ignoreRules(nil).appendFile(".", []byte(pattern+"\n"))
			for _, name := range paths {
				walkName := name
				if info, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(name))); statErr == nil && info.IsDir() {
					walkName += "/"
				}
				got := ignoreLikeWalker(rules, walkName)
				if got != ignored[name] {
					t.Errorf("pattern %q path %q: local=%v git=%v", pattern, name, got, ignored[name])
				}
			}
		})
	}
}

func ignoreLikeWalker(rules ignoreRules, name string) bool {
	directory := strings.HasSuffix(name, "/")
	name = strings.TrimSuffix(name, "/")
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		if rules.ignored(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return rules.ignored(name, directory)
}
