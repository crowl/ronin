package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/crowl/ronin/jev"
	"github.com/crowl/ronin/plugin"
	"github.com/crowl/ronin/telemetry"
)

var version = "dev"

func writeVersion(output io.Writer) error {
	_, err := fmt.Fprintf(output, "ronin %s\n", version)
	return err
}

func main() {
	os.Exit(run())
}

func run() int {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}

	if opts.version {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to write version: %v\n", err)
			return 1
		}
		return 0
	}

	// Plugins outlive the signal context so a final flush can complete
	// after cancellation. Plugins that fail to start are dropped.
	plugins := plugin.NewHost(telemetry.NewPlugin(), jev.NewPlugin())
	if err := plugins.Start(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "plugin initialization failed; continuing without it: %v\n", err)
	}
	defer closePlugins(plugins)

	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()
	ctx = plugin.NewContext(ctx, plugins)

	workflowCmd, workflowMode, err := parseWorkflowCommand(opts.args, os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if workflowMode {
		err = runWorkflowMode(ctx, opts, workflowCmd, os.Stdout)
	} else {
		err = runConversationMode(ctx, opts, os.Stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
