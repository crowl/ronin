package openai_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestCleanIncompleteEOFRetries(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		text := ""
		if requests == 2 {
			text = "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{}}}\n\n"
		}
		return streamResponse(strings.NewReader(text)), nil
	})
	client := newTestClient(t, &http.Client{Transport: transport})
	events, errs := client.PredictNext(t.Context(), llm.PredictNextRequest{})
	drainEvents(events)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestTerminalEventStopsReading(t *testing.T) {
	body := &terminalReader{Reader: strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{}}}\n\n")}
	client := newTestClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return streamResponse(body), nil })})
	events, errs := client.PredictNext(t.Context(), llm.PredictNextRequest{})
	drainEvents(events)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if body.overread {
		t.Fatal("read beyond terminal event")
	}
}

type terminalReader struct {
	*strings.Reader
	overread bool
}

func (r *terminalReader) Read(p []byte) (int, error) {
	if r.Len() == 0 {
		r.overread = true
		return 0, io.ErrUnexpectedEOF
	}
	return r.Reader.Read(p)
}
