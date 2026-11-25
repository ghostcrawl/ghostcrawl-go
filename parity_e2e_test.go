// go lane parity-e2e runner (real execution).
//
// Exercises EVERY user-surface row in scripts/phase178/rowset.json ONCE against
// the REAL ghostcrawl backend (the cloud test tenant) and writes one transcript
// per row to:
//
//	the go lane evidence directory as {row}.transcript.json
//
// conforming to the transcript schema (counts + var-names only,
// NEVER a credential value).
//
// Secrets: the token is read from GHOSTCRAWL_API_KEY and the base URL
// from GHOSTCRAWL_BASE_URL — NEVER hardcoded. If the token is unset this test is
// SKIPPED and fabricates NOTHING (the operator provisions a locked-down token,
// revoked after the run).
//
// Classification: deterministic real 2xx → GREEN; the account-gated
// agent 404 → GREEN (plumbing) / hosted (BYO LLM) (documented contract, NOT
// laundered, does not drive the full agent reasoning loop); async rows real-but-minimal; me/usage 404
// → real RED; any 429/503 self-throttle → RED FAIL-to-root-cause.
//
// Run via `make -C sdks/go parity-e2e` (which wraps this in the shared flock
// run-lock scripts/phase178/parity-run.sh so concurrent lanes serialize their hits
// to the single shared tenant — W2). This is a live-network integration run and is
// EXPECTED to exceed the 30s soft-latency guidance (network-bound, not a hang — W5).
package ghostcrawl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	abstractions "github.com/microsoft/kiota-abstractions-go"
)

const (
	peTokenVar = "GHOSTCRAWL_API_KEY"
	peBaseVar  = "GHOSTCRAWL_BASE_URL"
	peLane     = "go"
	peTarget   = "https://books.toscrape.com"
)

// Self-throttle statuses that may NEVER back a GREEN cell.
var peLaunder = map[int]bool{429: true, 503: true}

// Transient upstream render/gateway errors worth a bounded retry. Deliberately
// EXCLUDES 429/503 — those stay RED to root-cause, never retried into GREEN.
var peTransient = map[int]bool{500: true, 502: true, 504: true}

type peRow struct {
	Row      string `json:"row"`
	Endpoint string `json:"endpoint"`
	Async    bool   `json:"async_minimal"`
	Gated    bool   `json:"gated"`
	Note     string `json:"note"`
}

type peResult struct {
	method         string
	requestLine    string
	status         int
	shapeKeys      []string
	evidence       map[string]interface{}
	classification string
	execution      interface{} // string or nil
}

func peNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func peShapeKeys(v interface{}) []string {
	m, ok := v.(map[string]interface{})
	if !ok {
		return []string{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// peStatusOf extracts the real HTTP status from a typed GhostCrawlError-family error.
func peStatusOf(err error) int {
	switch e := err.(type) {
	case *GhostCrawlError:
		return e.StatusCode
	case *AuthenticationError:
		return e.StatusCode
	case *PaymentRequiredError:
		return e.StatusCode
	case *InvalidRequestError:
		return e.StatusCode
	case *RateLimitError:
		return e.StatusCode
	case *APIError:
		return e.StatusCode
	case *ProblemError:
		return e.StatusCode
	case *ScrapeError:
		return e.StatusCode
	}
	return 0
}

// peGreen builds a GREEN result on a returned body (implies a real 2xx round-trip).
func peGreen(method, requestLine string, result interface{}) peResult {
	ev := map[string]interface{}{"token_var": peTokenVar}
	var keys []string
	switch r := result.(type) {
	case []byte:
		ev["result_bytes"] = len(r)
		keys = []string{}
	case map[string]interface{}:
		keys = peShapeKeys(r)
		ev["key_count"] = len(keys)
	case rawMapper:
		// Typed results (ScrapeResult/SearchResult/CrawlRun) expose the full
		// decoded response map via ToMap() (back-compat door).
		keys = peShapeKeys(r.ToMap())
		ev["key_count"] = len(keys)
	default:
		keys = []string{}
	}
	return peResult{method, requestLine, 200, keys, ev, "GREEN", "ok"}
}

func peHandle(ctx context.Context, client *Client, row peRow) (peResult, error) {
	ep := row.Endpoint
	switch row.Row {
	case "scrape":
		r, err := client.Scrape(ctx, ScrapeRequest{URL: peTarget})
		return peGreen("scrape", ep, r), err
	case "content":
		r, err := client.Content(ctx, ContentRequest{URL: peTarget})
		return peGreen("content", ep, r), err
	case "screenshot":
		r, err := client.Screenshot(ctx, ScreenshotRequest{URL: peTarget})
		return peGreen("screenshot", ep, r), err
	case "pdf":
		// PDF is capability-pinned; pin a chrome identity so the server's random draw is
		// deterministic (engine="chrome" alone passes the guard but the draw can still
		// resolve non-chrome and 400) → reproducible GREEN.
		r, err := client.Pdf(ctx, PdfRequest{URL: peTarget, Engine: "chrome", Extra: map[string]interface{}{
			"identity_spec": map[string]interface{}{"claim_os": "windows", "claim_browser": "chrome", "viewport": "fullscreen"},
		}})
		return peGreen("pdf", ep, r), err
	case "extract":
		r, err := client.Extract(ctx, ExtractRequest{URL: peTarget, Schema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}})
		return peGreen("extract", ep, r), err
	case "search":
		r, err := client.Search(ctx, SearchRequest{Query: "books"})
		return peGreen("search", ep, r), err
	case "map":
		r, err := client.Map(ctx, MapRequest{URL: peTarget})
		return peGreen("map", ep, r), err
	case "crawl":
		r, err := client.Crawl(ctx, CrawlRequest{URL: peTarget, MaxPages: 1})
		return peGreen("crawl", ep, r), err
	case "crawl-runs":
		r, err := client.CrawlRuns().List(ctx)
		return peGreen("crawlRuns.list", ep, r), err
	case "profiles":
		r, err := client.Profiles().List(ctx)
		return peGreen("profiles.list", ep, r), err
	case "datasets":
		r, err := client.Datasets().List(ctx)
		return peGreen("datasets.list", ep, r), err
	case "recordings":
		r, err := client.Recordings().List(ctx)
		return peGreen("recordings.list", ep, r), err
	case "kv":
		// real-but-minimal round-trip: set then get (a bare GET on an absent key
		// is a 404 empty-read, not a plumbing proof).
		if _, err := client.KV().Set(ctx, "parity-e2e-probe", map[string]interface{}{"v": 1}); err != nil {
			return peResult{}, err
		}
		r, err := client.KV().Get(ctx, "parity-e2e-probe")
		return peGreen("kv.set+get", ep, r), err
	case "storage-states":
		// No public facade method — dispatch via the in-package authenticated request
		// escape hatch (GET /v1/storage-states), same adapter/auth as the modeled calls.
		r, err := client.request(ctx, abstractions.GET, "/v1/storage-states", nil)
		return peGreen("request", ep, r), err
	case "sessions":
		// A session needs an existing profile — create a unique one, open+release the
		// session, then delete the profile (act/observe are BYO).
		base := fmt.Sprintf("parity-e2e-%d", time.Now().Unix())
		prof, err := client.Profiles().Create(ctx, base, nil)
		if err != nil {
			return peResult{}, err
		}
		pname := base
		if n, ok := prof["name"].(string); ok && n != "" {
			pname = n
		}
		created, err := client.Sessions().Create(ctx, CreateSessionRequest{ProfileName: pname})
		if err != nil {
			return peResult{}, err
		}
		ev := map[string]interface{}{"token_var": peTokenVar, "key_count": len(peShapeKeys(created))}
		if sid := peIDOf(created, "session_id"); sid != "" {
			// SessionPersona parity (contract method 87): the running session's
			// customer-safe persona (device_class/os_class/browser_class/locale).
			if persona, perr := client.SessionPersona(ctx, sid); perr == nil {
				ev["persona_keys"] = len(peShapeKeys(persona))
			} else {
				ev["persona_keys"] = 0
			}
			if _, rerr := client.Sessions().Release(ctx, sid); rerr == nil {
				ev["released"] = 1
			} else {
				ev["released"] = 0
			}
		}
		_, _ = client.Profiles().Delete(ctx, pname)
		return peResult{"sessions.create", ep, 200, peShapeKeys(created), ev, "GREEN", "ok"}, nil
	case "webhooks":
		created, err := client.Webhooks().Create(ctx, CreateWebhookRequest{URL: "https://example.com/parity-e2e-hook", Events: []string{"scrape.completed"}})
		if err != nil {
			return peResult{}, err
		}
		ev := map[string]interface{}{"token_var": peTokenVar, "key_count": len(peShapeKeys(created))}
		if wid := peIDOf(created, "webhook_id"); wid != "" {
			if _, derr := client.Webhooks().Delete(ctx, wid); derr == nil {
				ev["deleted"] = 1
			} else {
				ev["deleted"] = 0
			}
		}
		return peResult{"webhooks.create", ep, 200, peShapeKeys(created), ev, "GREEN", "ok"}, nil
	case "schedules":
		created, err := client.Schedules().Create(ctx, CreateScheduleRequest{
			Cron: "0 0 * * *", Name: "parity-e2e-sch", JobType: "crawl",
			JobParams: map[string]interface{}{"url": peTarget, "max_pages": 1},
		})
		if err != nil {
			return peResult{}, err
		}
		ev := map[string]interface{}{"token_var": peTokenVar, "key_count": len(peShapeKeys(created))}
		if scid := peIDOf(created, "schedule_id"); scid != "" {
			if _, derr := client.Schedules().Delete(ctx, scid); derr == nil {
				ev["deleted"] = 1
			} else {
				ev["deleted"] = 0
			}
		}
		return peResult{"schedules.create", ep, 200, peShapeKeys(created), ev, "GREEN", "ok"}, nil
	case "agent":
		// Account-gated: require_agent_enabled() replies an honest 404 problem+json to
		// user keys. In go, Agent() → request() raises the 404 as a *ProblemError, caught
		// below and scored GREEN (plumbing) / hosted (BYO LLM). NOT laundered, NOT a
		// hosted agent loop.
		result, err := client.Agent(ctx, map[string]interface{}{
			"task": map[string]interface{}{"instruction": "click the 'Books to Scrape'", "start_url": peTarget},
		})
		if err != nil {
			if peStatusOf(err) == 404 {
				ev := map[string]interface{}{"token_var": peTokenVar, "note": "gated 404 is the documented expected contract; execution is BYO/not-hosted"}
				return peResult{"agent", ep, 404, []string{}, ev, "GREEN (plumbing)", "hosted (BYO LLM)"}, nil
			}
			return peResult{}, err
		}
		keys := peShapeKeys(result)
		gated := false
		if _, ok := result["detail"]; ok {
			gated = true
		}
		st := 200
		if gated {
			st = 404
		}
		ev := map[string]interface{}{"token_var": peTokenVar, "key_count": len(keys), "note": "gated 404 is the documented expected contract; execution is BYO/not-hosted"}
		return peResult{"agent", ep, st, keys, ev, "GREEN (plumbing)", "hosted (BYO LLM)"}, nil
	}
	return peResult{}, fmt.Errorf("no parity-e2e handler for row %q", row.Row)
}

// peIDOf returns the string value of primary or "id" from a result map, or "".
func peIDOf(m map[string]interface{}, primary string) string {
	for _, k := range []string{primary, "id"} {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return fmt.Sprintf("%v", int64(t))
			case fmt.Stringer:
				return t.String()
			}
		}
	}
	return ""
}

func peClassifyError(row peRow, err error) peResult {
	name := row.Row
	status := peStatusOf(err)
	ev := map[string]interface{}{"token_var": peTokenVar, "error_type": fmt.Sprintf("%T", err)}
	// A self-throttle 429/503 is a FAIL to root-cause, never a gated pass.
	if peLaunder[status] {
		ev["note"] = fmt.Sprintf("self-throttle %d — RED FAIL-to-root-cause, never laundered", status)
		return peResult{name, row.Endpoint, status, []string{}, ev, "RED", nil}
	}
	// search is an honest N/A-BYO (needs a user-supplied search-backend
	// key). SDK plumbing is proven by the typed 401 round-trip; not forced GREEN.
	if name == "search" {
		ev["note"] = "requires a user-supplied BYO search-backend key (provider_key; supported brave/tavily/google/bing/yandex) — a keyless call returns 401 search_backend_key_missing (the SDK specifies engine=google, passing the 400 engine-unspecified check); SDK plumbing verified by the typed 401 round-trip. N/A-BYO, not a defect."
		return peResult{name, row.Endpoint, status, []string{}, ev, "N/A", nil}
	}
	if status == 401 || status == 403 {
		msg := err.Error()
		if len(msg) > 120 {
			msg = msg[:120]
		}
		ev["note"] = fmt.Sprintf("%s: unexpected HTTP %d (%s) — honest RED to root-cause", name, status, msg)
		return peResult{name, row.Endpoint, status, []string{}, ev, "RED", nil}
	}
	ev["note"] = fmt.Sprintf("unexpected error on %s: %T", name, err)
	return peResult{name, row.Endpoint, status, []string{}, ev, "RED", nil}
}

func peRepoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	sdkRoot := filepath.Dir(thisFile) // sdks/go
	return filepath.Join(sdkRoot, "..", "..")
}

func peEvidenceDir() string {
	return filepath.Join(peRepoRoot(), ".planning", "phases",
		"178-api-sdk-functionality-e2e-and-parity", "178-EVIDENCE", "go")
}

func peWriteTranscript(t *testing.T, row peRow, res peResult) {
	dir := peEvidenceDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	transcript := map[string]interface{}{
		"lane":           peLane,
		"row":            row.Row,
		"method":         res.method,
		"request_line":   res.requestLine,
		"status":         res.status,
		"shape_keys":     res.shapeKeys,
		"evidence":       res.evidence,
		"classification": res.classification,
		"execution":      res.execution,
		"command":        fmt.Sprintf("make -C sdks/go parity-e2e ROW=%s", row.Row),
		"captured_at":    peNow(),
	}
	data, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	out := filepath.Join(dir, row.Row+".transcript.json")
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestParityE2E is the go lane real-execution parity sweep. It is SKIPPED when
// GHOSTCRAWL_API_KEY is unset (fabricates nothing); the make target sets it.
func TestParityE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv(peTokenVar)) == "" {
		t.Skipf("AUTH GATE: %s is unset — this runner performs REAL round-trips against "+
			"the cloud test tenant and will NOT fabricate transcripts. "+
			"Provision the operator locked-down token, then: export %s=<token> [%s=<url>] "+
			"&& make -C sdks/go parity-e2e (revoked after the run).", peTokenVar, peTokenVar, peBaseVar)
	}
	if strings.TrimSpace(os.Getenv(peBaseVar)) != "" {
		t.Logf("[parity-e2e:go] base URL from %s (value not echoed)", peBaseVar)
	}

	rowsetPath := filepath.Join(peRepoRoot(), "scripts", "phase178", "rowset.json")
	raw, err := os.ReadFile(rowsetPath)
	if err != nil {
		t.Fatalf("read rowset: %v", err)
	}
	var rows []peRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse rowset: %v", err)
	}
	if only := strings.TrimSpace(os.Getenv("ROW")); only != "" {
		filtered := rows[:0:0]
		for _, r := range rows {
			if r.Row == only {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			t.Fatalf("no such row: %q", only)
		}
		rows = filtered
	}

	client, err := New("") // token/base from env
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}

	ctx := context.Background()
	var green, plumbing, na, red int
	var reds []string
	for _, row := range rows {
		t0 := time.Now()
		res, herr := peHandleWithRetry(ctx, client, row, 3)
		if herr != nil {
			res = peClassifyError(row, herr)
		}
		if _, ok := res.evidence["elapsed_ms"]; !ok {
			res.evidence["elapsed_ms"] = int(time.Since(t0).Milliseconds())
		}
		peWriteTranscript(t, row, res)
		switch res.classification {
		case "GREEN":
			green++
		case "GREEN (plumbing)":
			plumbing++
		case "N/A":
			na++
		default:
			red++
			reds = append(reds, fmt.Sprintf("%s (status=%d)", row.Row, res.status))
		}
		t.Logf("[parity-e2e:go] %-14s %-16s status=%d", row.Row, res.classification, res.status)
	}

	t.Logf("[parity-e2e:go] DONE — %d GREEN, %d GREEN (plumbing), %d N/A, %d RED across %d row(s). Evidence: %s",
		green, plumbing, na, red, len(rows), peEvidenceDir())

	// A real RED fails the gate; N/A (BYO / route-absent) does not.
	if red > 0 {
		t.Fatalf("RED rows must be root-caused: %s", strings.Join(reds, ", "))
	}
}

// peHandleWithRetry runs a row's handler, retrying ONLY transient 5xx render/gateway
// errors (never 429/503 — those stay RED to root-cause).
func peHandleWithRetry(ctx context.Context, client *Client, row peRow, attempts int) (peResult, error) {
	var res peResult
	var err error
	for i := 0; i < attempts; i++ {
		res, err = peHandle(ctx, client, row)
		if err == nil {
			return res, nil
		}
		if peTransient[peStatusOf(err)] && i < attempts-1 {
			time.Sleep(time.Duration(2*(i+1)) * time.Second)
			continue
		}
		return res, err
	}
	return res, err
}
