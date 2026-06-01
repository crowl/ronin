package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Arguments interface {
	Validate() error
}

func DecodeArgs[A Arguments](rawArgs json.RawMessage) (A, error) {
	var args A

	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&args); err != nil {
		return args, fmt.Errorf("failed to decode arguments: %w", err)
	}
	if _, err := dec.Token(); err != nil {
		if err != io.EOF {
			return args, fmt.Errorf("failed to decode arguments: %w", err)
		}
	} else {
		return args, fmt.Errorf("failed to decode arguments: trailing JSON after arguments object")
	}

	if err := args.Validate(); err != nil {
		return args, fmt.Errorf("invalid arguments: %w", err)
	}

	return args, nil
}

type Tool[A Arguments, R Result] = func(context.Context, A) (R, error)

type Result interface {
	Artifacts() []Artifact
}

func CallTyped[A Arguments, R Result](ctx context.Context, rawArgs json.RawMessage, tool Tool[A, R]) (R, error) {
	args, err := DecodeArgs[A](rawArgs)
	if err != nil {
		var zero R
		return zero, err
	}
	return tool(ctx, args)
}

type TextArtifact struct {
	Text string
}

type ShellStream string

const (
	ShellStreamStdout ShellStream = "stdout"
	ShellStreamStderr ShellStream = "stderr"
)

type ShellStreamArtifact struct {
	Stream  ShellStream
	Content string
}

type FileArtifact struct{ Path, Content string }

type FileRangeArtifact struct {
	Path      string
	Content   string
	StartLine int
	EndLine   int
}

type FileMetadataArtifact struct {
	Path   string
	SHA256 string
}

type UnifiedDiffArtifact struct {
	Path string
	Diff string
}

type Artifact interface{ artifact() }

func (TextArtifact) artifact()         {}
func (ShellStreamArtifact) artifact()  {}
func (FileArtifact) artifact()         {}
func (FileRangeArtifact) artifact()    {}
func (FileMetadataArtifact) artifact() {}
func (UnifiedDiffArtifact) artifact()  {}
