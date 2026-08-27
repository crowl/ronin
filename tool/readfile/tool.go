package readfile

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/fsutil"
)

type ReadMode string

const (
	ReadModeAuto     = "auto"
	ReadModeMetadata = "metadata"
	ReadModeFull     = "full"

	defaultMaxReadBytes int64 = 128 * 1024
	maxReadBytes        int64 = 1024 * 1024
)

type Args struct {
	Path        string `json:"path" jsonschema:"Path to read. Relative paths resolve from the current working directory; absolute paths are allowed."`
	Mode        string `json:"mode,omitempty" jsonschema:"Read mode: auto, full, or metadata. Defaults to auto; auto may omit repeated unchanged full reads."`
	KnownSHA256 string `json:"known_sha256,omitempty" jsonschema:"Optional SHA-256 already known by the model. Auto mode may omit content when it matches."`
	Range       *Range `json:"range,omitempty" jsonschema:"Optional 1-based line range to return."`
	MaxBytes    int64  `json:"max_bytes,omitempty" jsonschema:"-"`
}

func (a Args) Validate() error {
	if strings.ContainsRune(a.Path, '\x00') {
		return tool.Error{
			Code:    "invalid_args",
			Message: "path must not contain NUL bytes",
		}
	}
	switch a.Mode {
	case "", ReadModeAuto, ReadModeMetadata, ReadModeFull:
	default:
		return tool.Error{
			Code:    "invalid_args",
			Message: "mode must be auto, metadata, or full",
		}
	}
	if a.Range != nil {
		if a.Range.StartLine < 0 || a.Range.EndLine < 0 {
			return tool.Error{
				Code:    "invalid_args",
				Message: "range line numbers must not be negative",
				Path:    a.Path,
			}
		}
		if a.Range.StartLine > 0 && a.Range.EndLine > 0 && a.Range.EndLine < a.Range.StartLine {
			return tool.Error{
				Code:    "invalid_args",
				Message: "range end_line must be greater than or equal to start_line",
				Path:    a.Path,
			}
		}
	}
	return nil
}

type Range struct {
	StartLine int `json:"start_line,omitempty" jsonschema:"Optional 1-based line number of the first line to return."`
	EndLine   int `json:"end_line,omitempty" jsonschema:"Optional 1-based line number of the last line to return."`
}

type Result struct {
	Path           string `json:"path"`
	FileID         string `json:"file_id"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Content        string `json:"content,omitempty"`
	ContentOmitted bool   `json:"content_omitted,omitempty"`
	OmittedReason  string `json:"omitted_reason,omitempty"`
	Range          *Range `json:"range,omitempty"`
}

func (r Result) Artifacts() []tool.Artifact {
	if r.ContentOmitted {
		return []tool.Artifact{
			tool.FileMetadataArtifact{
				Path:   r.Path,
				FileID: r.FileID,
			},
		}
	}
	if r.Range != nil {
		return []tool.Artifact{
			tool.FileRangeArtifact{
				Path:      r.Path,
				Content:   r.Content,
				StartLine: r.Range.StartLine,
				EndLine:   r.Range.EndLine,
			},
		}
	}
	return []tool.Artifact{
		tool.FileArtifact{
			Path:    r.Path,
			Content: r.Content,
		},
	}
}

func New(cwd string, cache *fsutil.ReadCache) *Tool {
	return &Tool{cwd: cwd, cache: cache}
}

type Tool struct {
	cwd   string
	cache *fsutil.ReadCache
}

func (t *Tool) Name() string {
	return "read_file"
}

func (t *Tool) Description() string {
	return "Read exact text file contents, a 1-based line range, or file metadata plus file_id and SHA-256 metadata. Prefer targeted ranges over whole-file reads when you know the relevant lines. Use before edit_file when you need exact text. Relative paths resolve from the current working directory; absolute paths are allowed. Auto mode may omit unchanged full-file content already returned."
}

func (t *Tool) Parameters() *jsonschema.Schema {
	return jsonschema.FromType[Args]()
}

func (t *Tool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	return tool.CallTyped(ctx, raw, t.call)
}

func (t *Tool) CallTitle(rawArgs json.RawMessage) (string, error) {
	args, err := tool.DecodeArgs[Args](rawArgs)
	if err != nil {
		return "", err
	}
	title := t.Name() + " " + args.Path
	if args.Range != nil {
		if args.Range.StartLine > 0 && args.Range.EndLine > 0 {
			title += fmt.Sprintf(" (%d:%d)", args.Range.StartLine, args.Range.EndLine)
		}
	}
	return title, nil
}

func (t *Tool) call(ctx context.Context, args Args) (Result, error) {
	if args.Mode == "" {
		args.Mode = ReadModeAuto
	}

	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	path, err := fsutil.ResolvePath(t.cwd, args.Path)
	if err != nil {
		return Result{}, err
	}

	if args.Mode == ReadModeMetadata {
		size, digest, err := readTextFileMetadata(ctx, path)
		if err != nil {
			return Result{}, err
		}
		sha := hex.EncodeToString(digest)
		entry := t.cache.GetOrCreate(path.Abs, sha)
		return Result{
			Path:           path.Display,
			FileID:         entry.ID,
			SHA256:         sha,
			Size:           size,
			ContentOmitted: true,
			OmittedReason:  "metadata mode requested",
			Range:          args.Range,
		}, nil
	}

	fullRead := args.Range == nil || (args.Range.StartLine == 0 && args.Range.EndLine == 0)

	var data []byte
	var digest []byte
	var content string
	var size int64

	if fullRead {
		data, size, digest, err = readTextFile(ctx, path, args.MaxBytes)
	} else {
		content, size, digest, err = readTextFileRange(ctx, path, *args.Range, args.MaxBytes)
	}
	if err != nil {
		return Result{}, err
	}

	sha := hex.EncodeToString(digest)
	entry := t.cache.GetOrCreate(path.Abs, sha)

	res := Result{
		Path:   path.Display,
		FileID: entry.ID,
		SHA256: sha,
		Size:   size,
		Range:  args.Range,
	}

	if args.Mode == ReadModeAuto && fullRead && entry.FullReturned {
		res.ContentOmitted = true
		res.OmittedReason = "same file content already returned as " + entry.ID
		return res, nil
	}

	if args.Mode == ReadModeAuto && fullRead && args.KnownSHA256 != "" && args.KnownSHA256 == sha {
		res.ContentOmitted = true
		res.OmittedReason = "known_sha256 matches current file SHA-256"
		return res, nil
	}

	if fullRead {
		content = string(data)
		entry = t.cache.MarkFull(path.Abs, sha)
		res.FileID = entry.ID
	}
	res.Content = content

	return res, nil
}

func readTextFile(ctx context.Context, path fsutil.ResolvedPath, maxBytes int64) ([]byte, int64, []byte, error) {
	file, info, err := openTextFile(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	limit := readLimit(maxBytes)
	if info.Size() > limit {
		return nil, 0, nil, tool.Error{Code: "file_too_large", Message: "file exceeds read limit", Path: path.Display}
	}

	select {
	case <-ctx.Done():
		return nil, 0, nil, ctx.Err()
	default:
	}

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, 0, nil, err
	}
	if int64(len(data)) > limit {
		return nil, 0, nil, tool.Error{Code: "file_too_large", Message: "file exceeds read limit", Path: path.Display}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, 0, nil, tool.Error{Code: "binary_file", Message: "file appears to be binary", Path: path.Display}
	}

	select {
	case <-ctx.Done():
		return nil, 0, nil, ctx.Err()
	default:
	}

	sum := sha256.Sum256(data)
	return data, info.Size(), sum[:], nil
}

func readTextFileMetadata(ctx context.Context, path fsutil.ResolvedPath) (int64, []byte, error) {
	file, info, err := openTextFile(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		default:
		}

		n, err := file.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			h.Write(chunk)
			if bytes.IndexByte(chunk, 0) >= 0 {
				return 0, nil, tool.Error{Code: "binary_file", Message: "file appears to be binary", Path: path.Display}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, nil, err
		}
	}

	return info.Size(), h.Sum(nil), nil
}

func readTextFileRange(ctx context.Context, path fsutil.ResolvedPath, r Range, maxBytes int64) (string, int64, []byte, error) {
	file, info, err := openTextFile(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	h := sha256.New()
	content, err := readRangeFromReader(ctx, file, h, r, maxBytes, path.Display)
	if err != nil {
		return "", 0, nil, err
	}

	return content, info.Size(), h.Sum(nil), nil
}

func openTextFile(path fsutil.ResolvedPath) (*os.File, os.FileInfo, error) {
	file, err := os.Open(path.Abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, tool.Error{Code: "file_not_found", Message: "file does not exist", Path: path.Display}
		}
		return nil, nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, nil, tool.Error{Code: "not_a_file", Message: "path is a directory", Path: path.Display}
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, tool.Error{Code: "not_a_file", Message: "path is not a regular file", Path: path.Display}
	}

	return file, info, nil
}

func readRangeFromReader(ctx context.Context, r io.Reader, h hash.Hash, lineRange Range, maxBytes int64, displayPath string) (string, error) {
	start := lineRange.StartLine
	if start <= 0 {
		start = 1
	}
	end := lineRange.EndLine
	limit := readLimit(maxBytes)

	var content strings.Builder
	reader := bufio.NewReaderSize(r, 32*1024)
	line := 1
	returnedBytes := int64(0)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			h.Write(part)
			if bytes.IndexByte(part, 0) >= 0 {
				return "", tool.Error{Code: "binary_file", Message: "file appears to be binary", Path: displayPath}
			}

			if line >= start && (end <= 0 || line <= end) {
				if returnedBytes+int64(len(part)) > limit {
					return "", tool.Error{Code: "file_too_large", Message: "range exceeds read limit", Path: displayPath}
				}
				content.Write(part)
				returnedBytes += int64(len(part))
			}
		}

		if err == nil {
			line++
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}

		return "", err
	}

	return content.String(), nil
}

func readLimit(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return defaultMaxReadBytes
	}
	if maxBytes > maxReadBytes {
		return maxReadBytes
	}
	return maxBytes
}
