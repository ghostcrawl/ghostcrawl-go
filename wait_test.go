package ghostcrawl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsTerminalStatus(t *testing.T) {
	for _, s := range []string{"completed", "failed", "cancelled", "canceled"} {
		if !isTerminalStatus(s) {
			t.Errorf("isTerminalStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "queued", "running", "pending", "in_progress"} {
		if isTerminalStatus(s) {
			t.Errorf("isTerminalStatus(%q) = true, want false", s)
		}
	}
}

func TestSecondsCeil(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int
	}{
		{0, 1},                    // never below 1
		{1 * time.Millisecond, 1}, // rounds up
		{999 * time.Millisecond, 1},
		{1 * time.Second, 1},
		{1500 * time.Millisecond, 2}, // rounds up
		{300 * time.Second, 300},
	}
	for _, c := range cases {
		if got := secondsCeil(c.in); got != c.want {
			t.Errorf("secondsCeil(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// WaitForCompletion must return ctx.Err() promptly when ctx is already done,
// without issuing any request.
func TestWaitForCompletion_ContextAlreadyCancelled(t *testing.T) {
	client, err := New("gck_live_test", "http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.CrawlRuns().WaitForCompletion(ctx, "run_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// WaitForCompletion long-polls the server-blocking GET until the run is
// terminal, re-arming across non-terminal windows — no client-side sleep — and
// sends wait=true with a timeout_s bounded by the ctx deadline.
func TestWaitForCompletion_LongPollAcrossWindows(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("wait") != "true" {
			t.Errorf("wait query = %q, want true", q.Get("wait"))
		}
		if q.Get("timeout_s") == "" {
			t.Errorf("timeout_s query missing")
		}
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			// Server window elapsed with the run still running → HTTP 200,
			// non-terminal body. The client must re-arm.
			_, _ = w.Write([]byte(`{"run_id":"run_1","status":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"run_id":"run_1","status":"completed","pages_crawled":7}`))
	}))
	defer srv.Close()

	client, err := New("gck_live_test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Small window so the deadline-capping path is exercised too.
	run, err := client.CrawlRuns().WaitForCompletion(ctx, "run_1", WithWaitWindow(2*time.Second))
	if err != nil {
		t.Fatalf("WaitForCompletion err = %v", err)
	}
	if status, _ := run["status"].(string); status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server calls = %d, want 3 (re-armed across two non-terminal windows)", got)
	}
}

// WaitForCompletion must not spin forever when the caller passes a deadline-less
// ctx (context.Background()) and the server keeps returning non-terminal 200s:
// it derives a default overall deadline and returns context.DeadlineExceeded.
func TestWaitForCompletion_BoundedDefaultDeadline(t *testing.T) {
	// Lower the default overall bound so the test is fast; restore after.
	orig := defaultCrawlWaitTimeout
	defaultCrawlWaitTimeout = 300 * time.Millisecond
	defer func() { defaultCrawlWaitTimeout = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		// Always non-terminal → without the default deadline this re-arms forever.
		_, _ = w.Write([]byte(`{"run_id":"run_1","status":"running"}`))
	}))
	defer srv.Close()

	client, err := New("gck_live_test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		// context.Background() has no deadline — the SDK must supply the bound.
		_, e := client.CrawlRuns().WaitForCompletion(
			context.Background(), "run_1", WithWaitWindow(50*time.Millisecond))
		done <- e
	}()

	select {
	case e := <-done:
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForCompletion did not honour the default deadline — spun past the bound")
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("expected at least one poll before the deadline")
	}
}

// A terminal failed/cancelled run is a successful observation (nil error); the
// caller inspects status.
func TestWaitForCompletion_TerminalFailedIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"run_1","status":"failed"}`))
	}))
	defer srv.Close()

	client, err := New("gck_live_test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	run, err := client.CrawlRuns().WaitForCompletion(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("err = %v, want nil (failed is a terminal observation)", err)
	}
	if status, _ := run["status"].(string); status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
}
