package ghostcrawl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Typed results expose the canonical fields AND keep the full decoded response
// reachable via Raw (back-compat door).
func TestTypedResults_FieldAccessAndRawFallback(t *testing.T) {
	raw := map[string]interface{}{
		"status":      "ok",
		"format":      "markdown",
		"markdown":    "# Hi",
		"content":     "# Hi",
		"status_code": float64(200), // JSON numbers decode to float64
		"bytes":       float64(4),
		"url":         "https://example.com",
		"warnings":    []interface{}{"w1"},
		// Undeclared server field — must survive on Raw.
		"identity_id": "id_123",
	}
	r := newScrapeResult(raw)
	if r.Status != "ok" || r.Format != "markdown" || r.Markdown != "# Hi" || r.Content != "# Hi" {
		t.Errorf("typed string fields wrong: %+v", r)
	}
	if r.StatusCode != 200 || r.Bytes != 4 {
		t.Errorf("typed numeric fields wrong: status_code=%d bytes=%d", r.StatusCode, r.Bytes)
	}
	if r.URL != "https://example.com" || len(r.Warnings) != 1 {
		t.Errorf("url/warnings wrong: %+v", r)
	}
	// Raw fallback: declared + undeclared fields both reachable.
	if got, _ := r.Raw["identity_id"].(string); got != "id_123" {
		t.Errorf("Raw[identity_id] = %q, want id_123", got)
	}
	if r.ToMap()["status"] != "ok" {
		t.Errorf("ToMap() lost the status field")
	}

	sr := newSearchResult(map[string]interface{}{"status": "completed", "query": "books", "results": []interface{}{1, 2}})
	if sr.Status != "completed" || sr.Query != "books" || len(sr.Results) != 2 {
		t.Errorf("SearchResult typed fields wrong: %+v", sr)
	}

	cr := newCrawlRun(map[string]interface{}{"run_id": "run_1", "status": "running"})
	if cr.RunID != "run_1" || cr.Status != "running" {
		t.Errorf("CrawlRun typed fields wrong: %+v", cr)
	}

	// Stable shape: a timed-out running handle ({status:"running", job_id}) has no
	// "results"/"items" — Results must still be a non-nil (empty) slice, never nil.
	rh := newSearchResult(map[string]interface{}{"status": "running", "job_id": "x"})
	if rh.Results == nil {
		t.Errorf("running-handle SearchResult.Results is nil; want empty non-nil slice")
	}
	if len(rh.Results) != 0 {
		t.Errorf("running-handle SearchResult.Results should be empty, got %+v", rh.Results)
	}
	if _, ok := rh.Raw["results"].([]interface{}); !ok {
		t.Errorf("running-handle Raw[results] should be a non-nil slice")
	}
}

// nil/empty maps must not panic and yield an empty (non-nil) typed result.
func TestTypedResults_NilSafe(t *testing.T) {
	r := newScrapeResult(nil)
	if r == nil || r.Raw == nil {
		t.Fatal("newScrapeResult(nil) must return a non-nil result with a non-nil Raw")
	}
	if r.Content != "" {
		t.Errorf("empty result Content = %q, want empty", r.Content)
	}
}

// pollSearchJob must follow poll_url, poll across 202 "running" windows honouring
// Retry-After, and return the terminal 200 body — a bounded client poll.
func TestPollSearchJob_202To200(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gck_live_test" {
			t.Errorf("Authorization = %q, want Bearer gck_live_test", r.Header.Get("Authorization"))
		}
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		if n < 3 {
			w.WriteHeader(http.StatusAccepted) // 202 running
			_, _ = w.Write([]byte(`{"status":"running"}`))
			return
		}
		w.WriteHeader(http.StatusOK) // terminal 200
		_, _ = w.Write([]byte(`{"status":"completed","results":[{"title":"t"}],"query":"books"}`))
	}))
	defer srv.Close()

	job := map[string]interface{}{"status": "running", "poll_url": srv.URL + "/v1/search/jobs/job_1"}
	final, err := pollSearchJob(context.Background(), srv.URL, job, searchPollOptions{
		headers: map[string]string{"Authorization": "Bearer gck_live_test", "Accept": "application/json"},
	})
	if err != nil {
		t.Fatalf("pollSearchJob err = %v", err)
	}
	if s, _ := final["status"].(string); s != "completed" {
		t.Errorf("final status = %q, want completed", s)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("poll GETs = %d, want 3 (two 202 windows + terminal 200)", got)
	}
}

// A cancelled ctx must abort the poll promptly with ctx.Err().
func TestPollSearchJob_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"running"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — the first inter-poll sleep must bail out

	job := map[string]interface{}{"status": "running", "poll_url": srv.URL + "/v1/search/jobs/job_1"}
	_, err := pollSearchJob(ctx, srv.URL, job, searchPollOptions{
		headers: map[string]string{"Accept": "application/json"},
	})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A 404 during the poll is terminal by default (poll_terminal_on_404) and surfaces
// a typed error rather than looping forever.
func TestPollSearchJob_404Terminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found"}`))
	}))
	defer srv.Close()

	job := map[string]interface{}{"status": "running", "poll_url": srv.URL + "/v1/search/jobs/job_1"}
	_, err := pollSearchJob(context.Background(), srv.URL, job, searchPollOptions{
		headers: map[string]string{"Accept": "application/json"},
		sleep:   func(context.Context, time.Duration) error { return nil }, // no real delay
	})
	if err == nil {
		t.Fatal("expected a terminal error on 404, got nil")
	}
}

// The warm path (no poll_url) returns the initial body immediately, unchanged.
func TestPollSearchJob_WarmNoPollURL(t *testing.T) {
	job := map[string]interface{}{"status": "completed", "results": []interface{}{}}
	out, err := pollSearchJob(context.Background(), "http://unused", job, searchPollOptions{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if s, _ := out["status"].(string); s != "completed" {
		t.Errorf("warm path status = %q, want completed", s)
	}
}

// Search(Wait:true) drives the end-to-end cold path: POST /v1/search returns a 202
// job with a poll_url, and the SDK polls it to a terminal result — the caller gets
// a typed *SearchResult with no poll code of its own.
func TestSearch_WaitDrivesColdPoll(t *testing.T) {
	var pollCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/search":
			w.WriteHeader(http.StatusOK)
			// Kiota reads the POST body; advertise a cold job with a poll handle.
			_, _ = w.Write([]byte(`{"status":"running","poll_url":"/v1/search/jobs/job_1","poll_max_seconds":30}`))
		case "/v1/search/jobs/job_1":
			atomic.AddInt32(&pollCalls, 1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"completed","query":"books","results":[{"title":"Book"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := New("gck_live_test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Search(context.Background(), SearchRequest{Query: "books", Wait: true})
	if err != nil {
		t.Fatalf("Search err = %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("status = %q, want completed", res.Status)
	}
	if len(res.Results) != 1 {
		t.Errorf("results len = %d, want 1", len(res.Results))
	}
	if atomic.LoadInt32(&pollCalls) < 1 {
		t.Errorf("poll endpoint was never hit — the cold wait did not poll")
	}
}

// A timed-out wait returns the raw running handle (no "query"/"results"); the
// facade must inject the caller's query/engine and a non-nil Results so the
// result carries a STABLE shape.
func TestSearch_TimeoutHandleHasStableShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/search":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"running","poll_url":"/v1/search/jobs/job_1","poll_max_seconds":30}`))
		case "/v1/search/jobs/job_1":
			// Never terminal — the poll budget elapses and returns this handle.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"running"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := New("gck_live_test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Search(context.Background(), SearchRequest{Query: "surprise-shape", Wait: true, WaitTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Search err = %v", err)
	}
	if res.Query != "surprise-shape" {
		t.Errorf("query = %q, want the caller's query injected", res.Query)
	}
	if res.Results == nil {
		t.Errorf("Results is nil; want non-nil empty slice")
	}
	if e, _ := res.Raw["engine"].(string); e != "google" {
		t.Errorf("engine = %q, want google injected", e)
	}
}
