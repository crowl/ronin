// Package agenttools constructs tools with explicit conversation and workspace lifetimes.
package agenttools

import (
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/tool/codenav"
	"github.com/crowl/ronin/tool/editfile"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/tool/writefile"
)

// Factory shares file mutation coordination, never conversation read history.
// It is safe to use concurrently. Each New call belongs to a fresh conversation.
type Factory struct{ mutations *fsutil.MutationQueue }

func NewFactory() *Factory { return &Factory{mutations: fsutil.NewMutationQueue()} }

// New creates tools for one conversation. Managed workspaces are restricted to
// file operations within their root; read-only agents cannot mutate or use MCP.
func (f *Factory) New(cwd string, readOnly, managed bool, mcp []runtime.Tool) []runtime.Tool {
	cache := fsutil.NewReadCache()
	reader := readfile.New(cwd, cache)
	if managed {
		reader = readfile.NewRestricted(cwd, cache)
	}
	if readOnly {
		return []runtime.Tool{reader, codenav.NewMap(cwd), codenav.NewFind(cwd)}
	}
	tools := []runtime.Tool{codenav.NewMap(cwd), codenav.NewFind(cwd), reader}
	if managed {
		return append(tools, editfile.NewRestricted(cwd, f.mutations), writefile.NewRestricted(cwd, f.mutations))
	}
	tools = append(tools, editfile.New(cwd, f.mutations), writefile.New(cwd, f.mutations), shell.New(cwd))
	return append(tools, mcp...)
}
