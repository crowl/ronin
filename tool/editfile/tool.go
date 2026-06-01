package editfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/crowl/ronin/diff"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
)

type Args struct {
	Path    string  `json:"path" jsonschema:"Path to edit. Relative paths resolve from the current working directory; absolute paths are allowed. File must already exist."`
	Patches []Patch `json:"patches" jsonschema:"Sequential exact text replacements. Later patches see earlier changes."`
}

func (a Args) Validate() error {
	if strings.ContainsRune(a.Path, '\x00') {
		return tool.Error{
			Code:    "invalid_args",
			Message: "path must not contain NUL bytes",
			Path:    a.Path,
		}
	}
	if len(a.Patches) == 0 {
		return tool.Error{
			Code:    "invalid_args",
			Message: "patches must not be empty",
			Path:    a.Path,
		}
	}
	for i, patch := range a.Patches {
		if patch.Old == "" {
			return tool.Error{
				Code:       "invalid_args",
				Message:    "patch old text must not be empty",
				Path:       a.Path,
				PatchIndex: new(i),
			}
		}
	}
	return nil
}

type Patch struct {
	Old        string `json:"old" jsonschema:"Exact existing text to replace. Must be unique unless replace_all is true."`
	New        string `json:"new" jsonschema:"Replacement text. Use an empty string to delete old text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"Replace every occurrence of old. Use only for mechanical repeated replacements."`
}

type Result struct {
	Path    string        `json:"path"`
	Patches []PatchResult `json:"patches"`
	Changed bool          `json:"changed"`

	// Used only for rendering, excluded from context
	UnifiedDiff string `json:"-"`
}

type PatchResult struct {
	Index        int  `json:"index"`
	Replacements int  `json:"replacements"`
	Changed      bool `json:"changed"`
}

func (r Result) Artifacts() []tool.Artifact {
	if r.UnifiedDiff == "" {
		return nil
	}
	return []tool.Artifact{
		tool.UnifiedDiffArtifact{
			Path: r.Path,
			Diff: r.UnifiedDiff,
		},
	}
}

type Tool struct {
	cwd   string
	queue *fsutil.MutationQueue
}

func New(cwd string, queue *fsutil.MutationQueue) *Tool {
	return &Tool{cwd: cwd, queue: queue}
}

func (t *Tool) Name() string {
	return "edit_file"
}

func (t *Tool) Description() string {
	return "Apply exact text replacements to an existing text file. Use for targeted edits after reading the file. Patches run in order; old text must match exactly and be unique unless replace_all is true."
}

func (t *Tool) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[Args]()
}

func (t *Tool) Call(ctx context.Context, rawArgs json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, rawArgs, t.call)
}

func (t *Tool) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[Args](rawArgs)
	if err != nil {
		return "", err
	}
	return t.Name() + " " + args.Path, nil
}

func (t *Tool) call(ctx context.Context, args Args) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	path, err := fsutil.ResolvePath(t.cwd, args.Path)
	if err != nil {
		return Result{}, err
	}

	var result Result
	err = t.queue.WithFile(ctx, path.Abs, func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		data, mode, err := readEditableFile(path)
		if err != nil {
			return err
		}

		next := append([]byte(nil), data...)
		results := make([]PatchResult, 0, len(args.Patches))

		for i, patch := range args.Patches {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			updated, count, err := applyPatch(next, patch)
			if err != nil {
				if terr, ok := errors.AsType[tool.Error](err); ok {
					terr.Path = path.Display
					terr.PatchIndex = new(i)
					return terr
				}
				return err
			}

			next = updated
			results = append(results, PatchResult{
				Index:        i,
				Replacements: count,
				Changed:      count > 0,
			})
		}

		result = Result{
			Path:    path.Display,
			Patches: results,
			Changed: false,
		}

		if bytes.Equal(data, next) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := fsutil.WriteFileAtomic(path.Abs, next, mode); err != nil {
			return err
		}

		var unifiedDiff string

		unifiedDiffBytes := diff.Diff("", data, "", next)
		if unifiedDiffBytes != nil {
			diffLines := strings.Split(string(unifiedDiffBytes), "\n")
			if len(diffLines) >= 3 {
				// Drop unified diff preamble
				diffLines = diffLines[3:]
			}
			unifiedDiff = strings.Join(diffLines, "\n")
		}

		result.Changed = true
		result.UnifiedDiff = unifiedDiff

		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return result, nil
}

func readEditableFile(path fsutil.ResolvedPath) ([]byte, os.FileMode, error) {
	info, err := os.Stat(path.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, tool.Error{Code: "file_not_found", Message: "file does not exist", Path: path.Display}
		}
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, tool.Error{Code: "not_a_file", Message: "path is a directory", Path: path.Display}
	}
	if !info.Mode().IsRegular() {
		return nil, 0, tool.Error{Code: "not_a_file", Message: "path is not a regular file", Path: path.Display}
	}

	data, err := os.ReadFile(path.Abs)
	if err != nil {
		return nil, 0, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, 0, tool.Error{Code: "binary_file", Message: "file appears to be binary", Path: path.Display}
	}

	return data, info.Mode(), nil
}

func applyPatch(data []byte, patch Patch) ([]byte, int, error) {
	old := []byte(patch.Old)
	newText := []byte(patch.New)

	count := bytes.Count(data, old)

	if count == 0 {
		return nil, 0, tool.Error{Code: "old_text_not_found", Message: "old text was not found"}
	}

	if !patch.ReplaceAll && count != 1 {
		return nil, 0, tool.Error{Code: "old_text_not_unique", Message: "old text occurs more than once; provide a larger unique old text or set replace_all=true"}
	}

	if patch.ReplaceAll {
		return bytes.ReplaceAll(data, old, newText), count, nil
	}

	return bytes.Replace(data, old, newText, 1), 1, nil
}
