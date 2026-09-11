package llm_test

import (
	"errors"
	"github.com/crowl/ronin/llm"
	"testing"
)

func TestHTTPContextClassification(t *testing.T) {
	for _, test := range []struct {
		status  int
		body    string
		context bool
	}{
		{400, `{"error":{"code":"context_length_exceeded"}}`, true},
		{400, "prompt is too long: 100 tokens > 90 maximum", true},
		{400, "input token count exceeds the maximum", true},
		{401, "context_length_exceeded", false},
		{400, `{"error":{"type":"invalid_request_error","message":"prompt is too long: 100 > 90"}}`, true},
		{400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"input token count exceeds the maximum"}}`, true},
		{400, `{"error":{"code":"invalid_schema","message":"bad schema"},"echo":"context_length_exceeded"}`, false},
		{400, `{"error":{"code":"context_length_exceeded","message":"opaque diagnostic"}}`, true},
		{400, "invalid tool schema", false},
	} {
		err := llm.HTTPError("test", test.status, test.body)
		if errors.Is(err, llm.ErrContextLimit) != test.context {
			t.Fatalf("%s: %v", test.body, err)
		}
	}
}
