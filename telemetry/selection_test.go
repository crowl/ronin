package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crowl/ronin/telemetry"
	"go.opentelemetry.io/otel"
)

func TestExporterSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", "application/x-protobuf") }))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	for _, tc := range []struct {
		name, traces, metrics, disabled  string
		wantTraces, wantMetrics, wantErr bool
	}{
		{name: "unset"},
		{name: "none", traces: "none", metrics: "none"},
		{name: "traces only", traces: "otlp", metrics: "none", wantTraces: true},
		{name: "metrics only", traces: "none", metrics: "otlp", wantMetrics: true},
		{name: "disabled overrides exporters", traces: "otlp", metrics: "otlp", disabled: "TRUE"},
		{name: "unsupported traces", traces: "console", wantErr: true},
		{name: "unsupported metrics", metrics: "prometheus", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_EXPORTER", tc.traces)
			t.Setenv("OTEL_METRICS_EXPORTER", tc.metrics)
			t.Setenv("OTEL_SDK_DISABLED", tc.disabled)
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
			// Disabled signals must not validate or initialize their exporter.
			for signal, enabled := range map[string]bool{"TRACES": tc.wantTraces, "METRICS": tc.wantMetrics} {
				protocol := "invalid"
				if enabled {
					protocol = "http/protobuf"
				}
				t.Setenv("OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL", protocol)
			}
			oldT, oldM := otel.GetTracerProvider(), otel.GetMeterProvider()
			defer func() {
				if otel.GetTracerProvider() != oldT {
					otel.SetTracerProvider(oldT)
				}
				if otel.GetMeterProvider() != oldM {
					otel.SetMeterProvider(oldM)
				}
			}()
			shutdown, err := telemetry.Setup(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Setup() error=%v", err)
			}
			if err != nil {
				return
			}
			if (otel.GetTracerProvider() != oldT) != tc.wantTraces {
				t.Fatal("wrong trace provider selection")
			}
			if (otel.GetMeterProvider() != oldM) != tc.wantMetrics {
				t.Fatal("wrong metric provider selection")
			}
			if err := shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
