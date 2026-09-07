package telemetry_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel"
	metricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

type traceCollector struct {
	tracev1.UnimplementedTraceServiceServer
	calls atomic.Int64
}

func (s *traceCollector) Export(context.Context, *tracev1.ExportTraceServiceRequest) (*tracev1.ExportTraceServiceResponse, error) {
	s.calls.Add(1)
	return &tracev1.ExportTraceServiceResponse{}, nil
}

type metricCollector struct {
	metricsv1.UnimplementedMetricsServiceServer
	calls atomic.Int64
}

func (s *metricCollector) Export(context.Context, *metricsv1.ExportMetricsServiceRequest) (*metricsv1.ExportMetricsServiceResponse, error) {
	s.calls.Add(1)
	return &metricsv1.ExportMetricsServiceResponse{}, nil
}

func TestOTLPGRPCExport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	traces, metrics := &traceCollector{}, &metricCollector{}
	tracev1.RegisterTraceServiceServer(server, traces)
	metricsv1.RegisterMetricsServiceServer(server, metrics)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { server.Stop(); <-done }()
	for _, key := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"} {
		t.Setenv(key, "")
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
	defer func() { otel.SetTracerProvider(oldT); otel.SetMeterProvider(oldM) }()
	shutdown, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, op := telemetry.Start(context.Background(), "request")
	op.End(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if traces.calls.Load() == 0 || metrics.calls.Load() == 0 {
		t.Fatal("missing gRPC export")
	}
}
