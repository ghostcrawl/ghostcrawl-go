package ghostcrawl

// SDK-owned bounded client-side poll for Search(..., Wait: true).
//
// Unlike crawl — whose GET /v1/crawl-runs/{id}?wait=true&timeout_s=N blocks
// SERVER-side on a completion event — the search-job contract is a CLIENT poll:
// POST /v1/search on a cold rotation returns
//
//	202 { status: "running", poll_url, deadline, poll_max_seconds, poll_terminal_on_404 }
//
// and GET {poll_url} returns 202 running (with a Retry-After header) until a
// terminal 200. The SDK owns the loop so the caller writes zero poll code — same
// Wait: true DX crawl has, different mechanism under the hood. This mirrors the
// Node reference (sdks/node/src/_searchPoll.ts) and the Python _search_poll.py.
//
// The poll talks plain net/http against the absolute (or base-relative) poll_url so
// it is unit-testable in isolation from the kiota generated core; it carries the
// same Bearer token via an explicit Authorization header.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// searchTerminalStates mirrors the Node SEARCH_TERMINAL_STATES / Python
// SEARCH_TERMINAL_STATES set.
var searchTerminalStates = map[string]bool{
	"completed": true,
	"done":      true,
	"failed":    true,
	"cancelled": true,
	"error":     true,
}

// defaultSearchTimeout is the overall client-side wait budget when the server
// gives no bound. defaultSearchPollInterval is the inter-poll fallback when the
// server sends no Retry-After.
const (
	defaultSearchTimeout      = 300 * time.Second
	defaultSearchPollInterval = 3 * time.Second
	// defaultSearchPollHTTPTimeout bounds a SINGLE poll GET. The overall wait is
	// bounded by the poll budget/deadline; this guards one hung connection so a
	// deadline-less ctx (context.Background()) can't wedge on a stuck socket.
	defaultSearchPollHTTPTimeout = 30 * time.Second
)

// defaultSearchPollClient is the bounded HTTP client used for poll GETs when the
// caller injects none — never http.DefaultClient, which has no timeout.
var defaultSearchPollClient = &http.Client{Timeout: defaultSearchPollHTTPTimeout}

// searchIsTerminal reports whether a search-job record has reached a terminal state.
func searchIsTerminal(job map[string]interface{}) bool {
	if job == nil {
		return false
	}
	s, _ := job["status"].(string)
	return searchTerminalStates[s]
}

// parseRetryAfter parses a Retry-After header value (delta-seconds) to a duration
// >= 0, or 0 when absent/invalid. (HTTP-date form is not used by the search-job
// contract, which advertises delta-seconds only.)
func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n * float64(time.Second))
}

// searchPollOptions configures pollSearchJob (injectable for tests).
type searchPollOptions struct {
	// headers are sent on every poll GET (Authorization + Accept).
	headers map[string]string
	// timeout overrides the overall client-side wait budget. Zero uses the
	// server's poll_max_seconds, falling back to defaultSearchTimeout.
	timeout time.Duration
	// httpClient is the HTTP client used for the poll GETs. Nil uses http.DefaultClient.
	httpClient *http.Client
	// sleep is the (ctx-aware) delay between polls. Nil uses ctxSleep.
	sleep func(ctx context.Context, d time.Duration) error
}

// ctxSleep sleeps for d, returning ctx.Err() if ctx is cancelled first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// resolveSearchPollURL resolves a poll_url that may be absolute or base-relative.
func resolveSearchPollURL(baseURL, pollURL string) string {
	if strings.HasPrefix(pollURL, "http://") || strings.HasPrefix(pollURL, "https://") {
		return pollURL
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(pollURL, "/")
}

// pollSearchJob bounded-polls a cold search job to a terminal result.
//
// It copies the STRUCTURE of CrawlRunsClient.WaitForCompletion (deadline + re-arm
// loop) but implements the client-poll contract: follows poll_url from the 202
// body, honours Retry-After between polls bounded by poll_max_seconds / an explicit
// timeout / defaultSearchTimeout, treats a terminal 200 (or a terminal-status body)
// as done, treats 404 as terminal when poll_terminal_on_404 (default true), returns
// the latest non-terminal record if the deadline elapses first, and respects ctx
// cancellation throughout. When the job carries no poll_url there is nothing to poll
// and the initial body is returned unchanged (warm path).
func pollSearchJob(ctx context.Context, baseURL string, job map[string]interface{}, opts searchPollOptions) (map[string]interface{}, error) {
	pollURLRaw, _ := job["poll_url"].(string)
	if pollURLRaw == "" {
		return job, nil
	}
	url := resolveSearchPollURL(baseURL, pollURLRaw)

	budget := opts.timeout
	if budget <= 0 {
		if pm := asInt(job["poll_max_seconds"]); pm > 0 {
			budget = time.Duration(pm) * time.Second
		} else {
			budget = defaultSearchTimeout
		}
	}
	terminalOn404 := true
	if v, ok := job["poll_terminal_on_404"].(bool); ok {
		terminalOn404 = v
	}

	hc := opts.httpClient
	if hc == nil {
		hc = defaultSearchPollClient
	}
	sleep := opts.sleep
	if sleep == nil {
		sleep = ctxSleep
	}

	deadline := time.Now().Add(budget)
	retryAfter := defaultSearchPollInterval
	latest := job

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return latest, nil
		}

		wait := retryAfter
		if wait > remaining {
			wait = remaining
		}
		if wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			if time.Now().After(deadline) {
				return latest, nil
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range opts.headers {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			if terminalOn404 {
				return nil, raiseForStatus(resp.StatusCode, string(body))
			}
			return latest, nil
		}
		if resp.StatusCode >= 400 {
			return nil, raiseForStatus(resp.StatusCode, string(body))
		}

		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil || data == nil {
			data = map[string]interface{}{}
		}
		if resp.StatusCode == http.StatusOK || searchIsTerminal(data) {
			return data, nil
		}
		// Still running (202) — re-arm with the server's next Retry-After.
		if len(data) > 0 {
			latest = data
		}
		if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			retryAfter = ra
		}
	}
}
