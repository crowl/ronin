package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/crowl/ronin/jev"
	"github.com/crowl/ronin/plugin"
	"github.com/crowl/ronin/plugin/guard"
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

	plugins, err := builtInPlugins(opts.disableJev)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Plugins outlive the signal context so a final flush can complete
	// after cancellation. Plugins that fail to start are dropped.
	host := plugin.NewHost(plugins...)
	if err := host.Start(context.Background()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "plugin initialization failed; continuing without it: %v\n", err)
	}
	defer closePlugins(host)

	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()
	ctx = plugin.NewContext(ctx, host)

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

func builtInPlugins(disableJev bool) ([]plugin.Plugin, error) {
	plugins := []plugin.Plugin{telemetry.NewPlugin()}
	if disableJev {
		return plugins, nil
	}

	apiKey := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("TYPESAFE_API_KEY is required unless -disable-jev is set")
	}
	client, err := jev.NewClient(apiKey)
	if err != nil {
		return nil, err
	}
	return append(plugins, guard.NewPlugin(client)), nil
}
