package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/tool"
)

func TestCallTyped(t *testing.T) {
	t.Run("preserves validation errors", func(t *testing.T) {
		_, err := tool.CallTyped(t.Context(), json.RawMessage(`{"name":""}`), func(context.Context, fakeArgs) (fakeResult, error) {
			return fakeResult{}, nil
		})
		if err == nil {
			t.Fatal("CallTyped() error = nil, want validation error")
		}

		var toolErr tool.Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("CallTyped() error = %v, want tool.Error", err)
		}
		if toolErr.Code != "invalid_args" {
			t.Fatalf("toolErr.Code = %q, want invalid_args", toolErr.Code)
		}
		if !strings.Contains(err.Error(), "invalid arguments") {
			t.Fatalf("CallTyped() error = %q, want invalid arguments context", err.Error())
		}
	})
	t.Run("rejects trailing JSON", func(t *testing.T) {
		_, err := tool.CallTyped(t.Context(), json.RawMessage(`{"name":"ok"} {"extra":true}`), func(context.Context, fakeArgs) (fakeResult, error) {
			return fakeResult{}, nil
		})
		if err == nil {
			t.Fatal("CallTyped() error = nil, want trailing JSON error")
		}
		if !strings.Contains(err.Error(), "trailing JSON") {
			t.Fatalf("CallTyped() error = %q, want trailing JSON context", err.Error())
		}
	})
}

func TestDecodeArgs(t *testing.T) {
	t.Run("decodes and validates arguments", func(t *testing.T) {
		args, err := tool.DecodeArgs[fakeArgs](json.RawMessage(`{"name":"ok"}`))
		if err != nil {
			t.Fatalf("DecodeArgs() error = %v", err)
		}
		if args.Name != "ok" {
			t.Fatalf("Name = %q, want ok", args.Name)
		}
	})
}

type fakeResult struct{}

func (fakeResult) Artifacts() []tool.Artifact {
	return nil
}

type fakeArgs struct {
	Name string `json:"name"`
}

func (a fakeArgs) Validate() error {
	if a.Name == "" {
		return tool.Error{Code: "invalid_args", Message: "name must not be empty"}
	}
	return nil
}
