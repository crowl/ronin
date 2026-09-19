package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jev"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/mcp"
	"github.com/crowl/ronin/plugin"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/telemetry"
	"github.com/crowl/ronin/tui"
	"github.com/crowl/ronin/workflow"
)

var version = "dev"
