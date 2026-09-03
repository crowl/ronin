package httpretry

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const (
	httpRetryMaxAttempts    = 5
	httpRetryErrorBodyLimit = 16 * 1024
	httpRetryBaseDelay      = 500 * time.Millisecond
	httpRetryMaxDelay       = 30 * time.Second
	httpRetryJitterMax      = 250 * time.Millisecond
)

// Do sends an HTTP request with the default LLM provider retry policy.
// The newRequest function is called for each attempt so request bodies can be replayed safely.
func Do(ctx context.Context, client *http.Client, newRequest func() (*http.Request, error)) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}

	for attempt := 1; attempt <= httpRetryMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req, err := newRequest()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests || attempt == httpRetryMaxAttempts {
			return resp, nil
		}

		delay := retryDelay(attempt, resp)
		discardAndClose(resp.Body)
		if err := sleepHTTPRetry(ctx, delay); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("request failed after %d attempts", httpRetryMaxAttempts)
}

func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if delay, ok := retryAfterDelay(resp.Header.Get("Retry-After")); ok {
			return min(delay, httpRetryMaxDelay)
		}
	}

	delay := httpRetryBaseDelay * (1 << (attempt - 1))
	delay = min(delay, httpRetryMaxDelay)
	return min(delay+jitterDuration(delay), httpRetryMaxDelay)
}

func retryAfterDelay(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(time.Until(when), 0), true
}

func jitterDuration(delay time.Duration) time.Duration {
	limit := min(delay/4, httpRetryJitterMax)
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(limit) + 1))
}

func sleepHTTPRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func discardAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, httpRetryErrorBodyLimit))
	_ = body.Close()
}
