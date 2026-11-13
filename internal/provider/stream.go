package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// preDeliveryError marks a failure that happened before any response byte
// reached the caller, after the inner retry budget was already spent.
// retryStream must not re-open the request for one of these — it is final,
// not transient.
type preDeliveryError struct{ err error }

func (e *preDeliveryError) Error() string { return e.err.Error() }
func (e *preDeliveryError) Unwrap() error { return e.err }

// openStream posts a JSON payload and hands back the raw streaming response for
// SSE consumption. Transient failures are retried with the same policy postJSON
// uses; once the response arrives the body is the caller's to read.
func openStream(ctx context.Context, url string, headers map[string]string, payload []byte) (*http.Response, error) {
	var (
		lastErr error
		delay   time.Duration
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && !sleepCtx(ctx, delay) {
			return nil, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := streamHTTPClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !isTransientNet(err) {
				return nil, &preDeliveryError{err}
			}
			lastErr = err
			dropIdleConnections()
			delay = retryDelay(attempt, nil)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = &httpError{status: resp.StatusCode, body: string(body), url: url}
			if retryableStatus(resp.StatusCode) {
				delay = retryDelay(attempt, resp)
				continue
			}
			return nil, &preDeliveryError{lastErr}
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("stream request failed")
	}
	return nil, &preDeliveryError{fmt.Errorf("%w (gave up after %d attempts)", lastErr, maxAttempts)}
}

// retryStream runs one streaming exchange and re-opens it when an attempt fails
// before any delta reached the caller. Once content has been delivered a retry
// would duplicate it in the transcript, so the failure is returned instead.
// Each attempt builds its accumulators from scratch, so a retry cannot inherit
// half-assembled text or tool calls.
func retryStream[T any](ctx context.Context, attempt func(delivered *bool) (T, error)) (T, error) {
	var zero T
	var (
		lastErr error
		delay   time.Duration
	)
	for i := 0; i < streamAttempts; i++ {
		if i > 0 && !sleepCtx(ctx, delay) {
			return zero, ctx.Err()
		}
		delivered := false
		out, err := attempt(&delivered)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		var pre *preDeliveryError
		if delivered || errors.As(err, &pre) || !isRetryable(err) {
			return zero, err
		}
		lastErr = err
		dropIdleConnections()
		delay = retryDelay(i, nil)
	}
	if lastErr == nil {
		lastErr = errors.New("stream request failed")
	}
	return zero, fmt.Errorf("%w (gave up after %d attempts)", lastErr, streamAttempts)
}

// iterateSSE walks an event stream, delivering each complete `data:` payload.
// Multi-line data frames are accumulated per the SSE spec; comment lines and
// blanks separate events.
func iterateSSE(body io.Reader, fn func(data string) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	var frame []string
	flush := func() error {
		if len(frame) == 0 {
			return nil
		}
		data := strings.Join(frame, "\n")
		frame = frame[:0]
		if data == "" || data == "[DONE]" {
			return nil
		}
		return fn(data)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			frame = append(frame, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
