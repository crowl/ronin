package runtime_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
)

func TestReadSuppressionContextLifecycle(t *testing.T) {
	for _, operation := range []string{"compact", "rewind", "fork", "new", "failed compact", "failed rewind"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			reader := readfile.New(dir, fsutil.NewReadCache())
			store := &fakeSessionStore{sessions: map[string]session.Session{"source": {ID: "source", WorkingDir: dir}}, messages: map[string][]llm.Message{}, activeID: "source"}
			compactor := &fakeCompactor{messages: []llm.Message{llm.UserMessage{Text: "summary"}}}
			if operation == "failed compact" {
				compactor.err = errors.New("compaction failed")
			}
			conversation, err := runtime.NewConversation(runtime.ConversationConfig{
				ModelClient: &fakeModelClient{}, Tools: []runtime.Tool{reader}, Compactor: compactor,
				SessionStore: store, Session: store.sessions["source"], Messages: []llm.Message{llm.UserMessage{Text: "request"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			read := func() readfile.Result {
				t.Helper()
				result, err := reader.Call(t.Context(), json.RawMessage(`{"path":"source.txt"}`))
				if err != nil {
					t.Fatal(err)
				}
				return result.(readfile.Result)
			}
			first := read()
			if first.Content != "source\n" || !read().ContentOmitted {
				t.Fatal("expected initial content followed by suppression")
			}
			switch operation {
			case "compact", "failed compact":
				err = conversation.CompactConversation(t.Context())
			case "rewind":
				err = conversation.Rewind(t.Context(), conversation.RewindPoints()[0])
			case "fork":
				err = conversation.Fork(t.Context(), conversation.RewindPoints()[0])
			case "new":
				err = conversation.NewConversation()
			case "failed rewind":
				err = conversation.Rewind(t.Context(), runtime.RewindPoint{MessageIndex: -1})
			}
			failed := operation == "failed compact" || operation == "failed rewind"
			if (err != nil) != failed {
				t.Fatalf("operation error = %v", err)
			}
			next := read()
			if next.ContentOmitted != failed {
				t.Fatalf("ContentOmitted = %v, want %v", next.ContentOmitted, failed)
			}
			if !failed && next.Content != first.Content {
				t.Fatal("content unavailable after context reset")
			}
			if next.FileID != first.FileID {
				t.Fatal("context reset changed file identity")
			}
			if !read().ContentOmitted {
				t.Fatal("subsequent read should be suppressed again")
			}
		})
	}
}
