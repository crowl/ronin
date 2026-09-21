package telemetry_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/crowl/ronin/telemetry"
)

func TestCollectorFailureDoesNotBlockOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	for _, key := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"} {
		t.Setenv(key, "")
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
	defer func() { otel.SetTracerProvider(oldT); otel.SetMeterProvider(oldM) }()
	shutdown, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, op := telemetry.Start(context.Background(), "tool"); op.End(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operation blocked on collector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := shutdown(ctx); err == nil {
		t.Fatal("expected failed flush")
	}
}

func TestUnavailableCollectorLogsToFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "OTEL_EXPORTER_OTLP_METRICS_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:1/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "1")
	oldT, oldH := otel.GetTracerProvider(), otel.GetErrorHandler()
	defer func() { otel.SetTracerProvider(oldT); otel.SetErrorHandler(oldH) }()

	errOut := bytes.NewBuffer(nil)
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = errOut.ReadFrom(r)
	}()

	shutdown, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, op := telemetry.Start(context.Background(), "request")
	op.End(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdown(ctx)

	_ = w.Close()
	os.Stderr = oldStderr
	<-done
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want telemetry errors kept off stderr", errOut.String())
	}
	logged, err := os.ReadFile(filepath.Join(data, "ronin", "logs", "telemetry.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(logged)) == 0 {
		t.Fatal("telemetry log is empty, want the unavailable collector recorded")
	}
}
