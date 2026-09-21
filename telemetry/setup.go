// Package telemetry provides best-effort OpenTelemetry execution instrumentation.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/crowl/ronin/config"
)

// Setup configures global providers using OTEL_TRACES_EXPORTER and
// OTEL_METRICS_EXPORTER (otlp or none; unset defaults to none). The caller
// owns shutdown. Export errors never become execution errors. OTLP exporters read
// standard endpoint, header, TLS and timeout environment variables themselves.
//
// When export is enabled, SDK errors (including an unavailable collector) are
// appended to a log file under the data directory instead of stderr, so a TUI
// session is not corrupted by export retries.
func Setup(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return noop, nil
	}
	enabled := make(map[string]bool)
	for _, signal := range []string{"TRACES", "METRICS"} {
		switch value := os.Getenv("OTEL_" + signal + "_EXPORTER"); value {
		case "", "none":
		case "otlp":
			enabled[signal] = true
		default:
			return noop, fmt.Errorf("unsupported OTEL_%s_EXPORTER %q: use otlp or none", signal, value)
		}
	}
	if len(enabled) == 0 {
		return noop, nil
	}
	errLog, err := newErrorLog()
	if err != nil {
		return noop, err
	}
	otel.SetErrorHandler(errLog)
	fail := func(err error) (func(context.Context) error, error) {
		_ = errLog.Close()
		return noop, err
	}
	protocol := func(signal string) string {
		if v := os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"); v != "" {
			return v
		}
		if v := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"); v != "" {
			return v
		}
		return "http/protobuf"
	}
	for signal := range enabled {
		if p := protocol(signal); p != "grpc" && p != "http/protobuf" {
			return fail(fmt.Errorf("unsupported OTLP %s protocol %q", strings.ToLower(signal), p))
		}
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", "ronin")), resource.WithFromEnv())
	if err != nil {
		return fail(err)
	}
	var te sdktrace.SpanExporter
	if enabled["TRACES"] {
		if protocol("TRACES") == "grpc" {
			te, err = otlptracegrpc.New(ctx)
		} else {
			te, err = otlptracehttp.New(ctx)
		}
	}
	if err != nil {
		return fail(err)
	}
	var me sdkmetric.Exporter
	if enabled["METRICS"] {
		if protocol("METRICS") == "grpc" {
			me, err = otlpmetricgrpc.New(ctx)
		} else {
			me, err = otlpmetrichttp.New(ctx)
		}
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if te != nil {
			_ = te.Shutdown(cleanup)
		}
		return fail(err)
	}
	var shutdowns []func(context.Context) error
	if te != nil {
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(te))
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}
	if me != nil {
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)))
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}
	return func(ctx context.Context) error {
		var errs []error
		for _, shutdown := range shutdowns {
			errs = append(errs, shutdown(ctx))
		}
		errs = append(errs, errLog.Close())
		return errors.Join(errs...)
	}, nil
}

// errorLog is the process-wide OpenTelemetry error handler. The SDK default
// writes every export failure to stderr; this appends them to a file instead.
type errorLog struct {
	mu     sync.Mutex
	file   *os.File
	logger *log.Logger
}

func newErrorLog() (*errorLog, error) {
	dir, err := config.EnsureDataDir()
	if err != nil {
		return nil, fmt.Errorf("telemetry log: %w", err)
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create telemetry log directory %q: %w", logDir, err)
	}
	f, err := os.OpenFile(filepath.Join(logDir, "telemetry.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open telemetry log: %w", err)
	}
	return &errorLog{file: f, logger: log.New(f, "", log.LstdFlags)}, nil
}

func (l *errorLog) Handle(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logger != nil {
		l.logger.Println(err)
	}
}

func (l *errorLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger = nil
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
