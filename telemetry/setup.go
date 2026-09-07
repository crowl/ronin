// Package telemetry provides best-effort OpenTelemetry execution instrumentation.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
)

// Setup configures global providers using OTEL_TRACES_EXPORTER and
// OTEL_METRICS_EXPORTER (otlp or none; unset defaults to none). The caller
// owns shutdown. Export errors never become execution errors. OTLP exporters read
// standard endpoint, header, TLS and timeout environment variables themselves.
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
			return noop, fmt.Errorf("unsupported OTLP %s protocol %q", strings.ToLower(signal), p)
		}
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", "ronin")), resource.WithFromEnv())
	if err != nil {
		return noop, err
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
		return noop, err
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
		return noop, err
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
		return errors.Join(errs...)
	}, nil
}
