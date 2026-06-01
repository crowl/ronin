package writefile

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
)

type Args struct {
	Path    string `json:"path" jsonschema:"Path to create or overwrite. Relative paths resolve from the current working directory; absolute paths are allowed."`
	Content string `json:"content" jsonschema:"Complete desired file content. This replaces the whole file."`
}

func (a Args) Validate() error {
	if strings.ContainsRune(a.Path, '\x00') {
		return tool.Error{Code: "invalid_args", Message: "path must not contain NUL bytes"}
	}
	return nil
}

type Result struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Changed bool   `json:"changed"`
	Bytes   int    `json:"bytes"`

	// Used only for rendering, excluded from context
	Content string `json:"-"`
}

func (r Result) Artifacts() []tool.Artifact {
	if r.Content == "" {
		return nil
	}
	return []tool.Artifact{
		tool.FileArtifact{
			Path:    r.Path,
			Content: r.Content,
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
	return "write_file"
}

func (t *Tool) Description() string {
	return "Create or completely overwrite a file. Use for new files or intentional full rewrites only; prefer edit_file for targeted changes. Parent directories are created automatically."
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

	path, err := fsutil.ResolvePathForWrite(t.cwd, args.Path)
	if err != nil {
		return Result{}, err
	}

	var result Result
	err = t.queue.WithPath(ctx, path.Abs, func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		content := []byte(args.Content)
		mode := fs.FileMode(0o644)
		created := false
		changed := true

		info, err := os.Lstat(path.Abs)
		if err != nil {
			if os.IsNotExist(err) {
				created = true
			} else {
				return err
			}
		} else {
			if info.IsDir() {
				return tool.Error{Code: "not_a_file", Message: "path is a directory", Path: path.Display}
			}
			if info.Mode().IsRegular() {
				mode = info.Mode()

				currentData, err := os.ReadFile(path.Abs)
				if err != nil {
					return err
				}
				changed = !bytes.Equal(currentData, content)
			}
		}

		if err := os.MkdirAll(filepath.Dir(path.Abs), 0o755); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if changed || created {
			if err := fsutil.WriteFileAtomic(path.Abs, content, mode); err != nil {
				return err
			}
		}

		result = Result{
			Path:    path.Display,
			Created: created,
			Changed: changed || created,
			Bytes:   len(content),
			Content: string(content),
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return result, nil
}
