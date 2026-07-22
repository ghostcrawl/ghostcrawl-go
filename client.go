// Package ghostcrawl provides the official Go client for the GhostCrawl API.
// Collect web data at scale — scrape, crawl, search, and extract structured data.
//
// Architecture:
//
//	_generated/ Kiota core  — spec-faithful 98-op request-builder (models, transport, auth)
//	This FACADE              — thin idiomatic layer delegating to the generated builders
//
// All HTTP transport, URL routing, serialization, and auth come from the generated core.
// The facade maps idiomatic calls (client.Scrape) to generated builders via
// BaseBearerTokenAuthenticationProvider + NetHttpRequestAdapter, and returns plain
// map[string]interface{} for JSON-serializable responses.
//
// Usage:
//
//	client, err := ghostcrawl.New("gck_live_YOUR_KEY")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	result, err := client.Scrape(context.Background(), ghostcrawl.ScrapeRequest{URL: "https://example.com"})
package ghostcrawl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	abstractions "github.com/microsoft/kiota-abstractions-go"
	absauth "github.com/microsoft/kiota-abstractions-go/authentication"
	absser "github.com/microsoft/kiota-abstractions-go/serialization"
	khttp "github.com/microsoft/kiota-http-go"
	kjson "github.com/microsoft/kiota-serialization-json-go"

	genmodels "github.com/GhostCrawl/ghostcrawl-go/v2/models"
	genv1 "github.com/GhostCrawl/ghostcrawl-go/v2/v1"
)

const defaultBaseURL = "https://api.ghostcrawl.io"

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// GhostCrawlError is the base error for all GhostCrawl API errors.
//
// In addition to the HTTP details, it carries the canonical error fields from
// the catalog (codes.json) so every typed error — including the legacy
// status-keyed ones — exposes a machine-readable Code, the Retryable flag, and
// the originating RequestID.
type GhostCrawlError struct {
	Message    string
	StatusCode int
	Body       string
	// Code is the canonical error code from the catalog (e.g. "rate_limited").
	// Empty only for transport-level errors that never reached the API.
	Code string
	// Retryable reports whether retrying the same call may succeed.
	Retryable bool
	// RequestID echoes the response request id (problem+json `instance`), when present.
	RequestID string
}

func (e *GhostCrawlError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("ghostcrawl: %s (%s, status %d)", e.Message, e.Code, e.StatusCode)
	}
	return fmt.Sprintf("ghostcrawl: %s (status %d)", e.Message, e.StatusCode)
}

// AuthenticationError is returned on 401 — missing or invalid API key.
type AuthenticationError struct{ *GhostCrawlError }

// PaymentRequiredError is returned on 402 — usage or spend limit reached.
type PaymentRequiredError struct{ *GhostCrawlError }

// InvalidRequestError is returned on 422 — bad request parameters.
type InvalidRequestError struct{ *GhostCrawlError }

// RateLimitError is returned on 429 — rate limit reached.
type RateLimitError struct{ *GhostCrawlError }

// APIError is returned on 5xx server errors.
type APIError struct{ *GhostCrawlError }

// ProblemError is returned for an OUR-side failure delivered as a non-2xx
// application/problem+json response (channel "problem" in the catalog) that does
// not map to one of the legacy status-keyed types above. The canonical Code,
// Retryable flag, and RequestID are available on the embedded GhostCrawlError.
type ProblemError struct{ *GhostCrawlError }

// ScrapeError is returned when a scrape/crawl call completes with HTTP 200 but
// the result itself reports a TARGET failure (channel "result" in the catalog):
// the result object had ok:false or a non-empty result_error. This is what stops
// a blocked or timed-out scrape from being silently counted as a success.
//
// Code, Retryable, and RequestID are on the embedded GhostCrawlError.
type ScrapeError struct {
	*GhostCrawlError
	// TargetStatus is the HTTP status the target site returned, when the failure
	// was target_http_error. Zero when not applicable.
	TargetStatus int
}

// problemBody is the subset of an application/problem+json body the SDK reads.
type problemBody struct {
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	Title     string `json:"title"`
	Instance  string `json:"instance"`
	Retryable *bool  `json:"retryable"`
}

// raiseForStatus builds a typed error for a non-2xx response. When body is a
// parseable application/problem+json payload it keys the ProblemError on the
// canonical `code`; otherwise it falls back to the status→code mapping (some
// transports discard the response body for unmapped statuses, leaving only the
// status line). The legacy status-keyed typed errors (AuthenticationError, …)
// are still returned for the statuses they cover so existing callers keep working.
func raiseForStatus(statusCode int, body string) error {
	if statusCode < 400 {
		return nil
	}

	// Prefer the canonical code from a problem+json body, falling back to the
	// status→code mapping when the body is absent/opaque (some transports discard
	// the body for unmapped statuses, leaving only the status line).
	code := codeForStatus(statusCode)
	requestID := ""
	if pb, ok := parseProblemJSON(body); ok {
		if pb.Code != "" {
			code = pb.Code
		}
		requestID = pb.Instance
	}
	base := &GhostCrawlError{
		StatusCode: statusCode,
		Body:       body,
		Code:       code,
		Retryable:  isRetryableCode(code),
		RequestID:  requestID,
	}

	switch statusCode {
	case 401:
		base.Message = "Authentication failed — check your API key."
		return &AuthenticationError{base}
	case 402:
		base.Message = "Usage or spend limit reached."
		return &PaymentRequiredError{base}
	case 422:
		base.Message = fmt.Sprintf("Invalid request: %s", body)
		return &InvalidRequestError{base}
	case 429:
		base.Message = "Rate limit reached — retry after a short delay."
		return &RateLimitError{base}
	default:
		if statusCode >= 500 {
			base.Message = fmt.Sprintf("Server error: %d", statusCode)
			return &APIError{base}
		}
		base.Message = fmt.Sprintf("Request failed: %d", statusCode)
		return &ProblemError{base}
	}
}

// parseProblemJSON attempts to read an application/problem+json body. It returns
// ok=false when s is not a JSON object (e.g. a Kiota status-only message string).
func parseProblemJSON(s string) (problemBody, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") {
		return problemBody{}, false
	}
	var pb problemBody
	if err := json.Unmarshal([]byte(trimmed), &pb); err != nil {
		return problemBody{}, false
	}
	return pb, true
}

// wrapAPIError wraps Kiota API errors into typed GhostCrawl errors.
func wrapAPIError(err error) error {
	if err == nil {
		return nil
	}
	type apiErr interface {
		GetStatusCode() int
		Error() string
	}
	if ae, ok := err.(apiErr); ok {
		return raiseForStatus(ae.GetStatusCode(), ae.Error())
	}
	return fmt.Errorf("ghostcrawl: %w", err)
}

// scanResultError inspects a successful (HTTP 200) result map for a TARGET
// failure and returns a *ScrapeError when the result is not ok. A result is
// treated as failed when it carries ok:false OR a result_error object OR a
// top-level non-success `code` from the result channel of the catalog. Returns
// nil when the result represents a real success, so a normal scrape is
// unaffected.
func scanResultError(result map[string]interface{}) *ScrapeError {
	if result == nil {
		return nil
	}

	// Descend into a `results` envelope (scrape/extract wrap per-URL results) — the
	// target failure lives on the INNER result, not the envelope top level.
	if rows, ok := result["results"].([]interface{}); ok {
		for _, row := range rows {
			if m, ok := row.(map[string]interface{}); ok {
				if se := scanResultError(m); se != nil {
					return se
				}
			}
		}
		return nil
	}

	// Pull the canonical code from result_error{code,...} first, then a
	// top-level `code` (the worker stamps both).
	var (
		code         string
		retryable    bool
		retryableSet bool
		targetStatus int
		requestID    string
	)

	if re, ok := result["result_error"].(map[string]interface{}); ok && re != nil {
		code, _ = re["code"].(string)
		if rb, ok := re["retryable"].(bool); ok {
			retryable, retryableSet = rb, true
		}
		targetStatus = asInt(re["target_status"])
	}
	if code == "" {
		// Only treat a top-level `code` as an error when it is a known
		// result-channel code (a successful response may carry other fields).
		if tc, ok := result["code"].(string); ok {
			if spec, known := errorCatalog[tc]; known && spec.Channel == ChannelResult {
				code = tc
			}
		}
	}

	okVal, okPresent := result["ok"].(bool)
	// The flat markdown-build envelope reports a target failure ONLY via
	// status="failed" (no ok/result_error) — don't count it as a success.
	statusFailed := false
	if s, ok := result["status"].(string); ok && s == "failed" {
		statusFailed = true
	}
	failed := code != "" || (okPresent && !okVal) || statusFailed
	if !failed {
		return nil
	}
	if code == "" {
		// ok:false with no explicit code — surface a generic result failure.
		code = CodeEmptyContent
	}
	if !retryableSet {
		retryable = isRetryableCode(code)
	}
	if requestID == "" {
		requestID, _ = result["request_id"].(string)
	}
	if targetStatus == 0 {
		targetStatus = asInt(result["target_status"])
	}

	base := &GhostCrawlError{
		Message:    messageForResultCode(code),
		StatusCode: 200,
		Code:       code,
		Retryable:  retryable,
		RequestID:  requestID,
	}
	return &ScrapeError{GhostCrawlError: base, TargetStatus: targetStatus}
}

// normalizeScrapeContent mirrors the format-specific rendered page value
// ("markdown"/"html"/"text") onto a stable "content" key so callers can read
// result["content"] regardless of the requested format. It looks first at the
// top level (the flat markdown-build envelope) and then inside the first row of
// a `results` envelope (the html/default fleet path renders content per-URL, not
// at the top level). The original format-specific key is preserved (backward
// compatible). No-op when result is nil or already carries a "content" key.
func normalizeScrapeContent(result map[string]interface{}) {
	if result == nil {
		return
	}
	if _, ok := result["content"]; ok {
		return
	}
	value := scrapeContentFromMap(result)
	if value == "" {
		// html/default path: content lives on the first per-URL result row.
		if rows, ok := result["results"].([]interface{}); ok && len(rows) > 0 {
			if row, ok := rows[0].(map[string]interface{}); ok {
				value = scrapeContentFromMap(row)
			}
		}
	}
	if value != "" {
		result["content"] = value
	}
}

// scrapeContentFromMap pulls the rendered page value from a single result map,
// preferring the format-specific key and falling back to markdown/html/text.
func scrapeContentFromMap(m map[string]interface{}) string {
	if fmt, _ := m["format"].(string); fmt != "" {
		if v, ok := m[fmt].(string); ok && v != "" {
			return v
		}
	}
	for _, k := range []string{"markdown", "html", "text"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// asInt coerces a JSON-decoded number (which may be int, int64, or float64) to
// an int. Returns 0 for nil/non-numeric.
func asInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// messageForResultCode returns a short, customer-facing message for a result code.
func messageForResultCode(code string) string {
	switch code {
	case CodeBlocked:
		return "Blocked by the target's anti-bot protection — retry with a different identity or proxy."
	case CodeCaptchaRequired:
		return "The target presented a CAPTCHA."
	case CodeTargetHTTPError:
		return "The target returned an HTTP error."
	case CodeNavigationFailed:
		return "Could not reach the target."
	case CodeEmptyContent:
		return "No extractable content was returned."
	default:
		return fmt.Sprintf("Scrape failed: %s", code)
	}
}

// ---------------------------------------------------------------------------
// Static bearer token provider — implements absauth.AccessTokenProvider
// ---------------------------------------------------------------------------

type staticTokenProvider struct {
	token     string
	validator *absauth.AllowedHostsValidator
}

func newStaticTokenProvider(token string) *staticTokenProvider {
	v := absauth.NewAllowedHostsValidator([]string{})
	return &staticTokenProvider{token: token, validator: &v}
}

func (p *staticTokenProvider) GetAuthorizationToken(_ context.Context, _ *url.URL, _ map[string]interface{}) (string, error) {
	return p.token, nil
}

func (p *staticTokenProvider) GetAllowedHostsValidator() *absauth.AllowedHostsValidator {
	return p.validator
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// untypedToAny recursively converts a Kiota UntypedNodeable response to a plain Go value.
func untypedToAny(node absser.UntypedNodeable) interface{} {
	if node == nil {
		return nil
	}
	switch v := node.(type) {
	case *absser.UntypedObject:
		props := v.GetValue()
		result := make(map[string]interface{}, len(props))
		for k, prop := range props {
			result[k] = untypedToAny(prop)
		}
		return result
	case *absser.UntypedArray:
		items := v.GetValue()
		result := make([]interface{}, len(items))
		for i, item := range items {
			result[i] = untypedToAny(item)
		}
		return result
	case *absser.UntypedString:
		s := v.GetValue()
		if s == nil {
			return nil
		}
		return *s
	case *absser.UntypedInteger:
		val := v.GetValue()
		if val == nil {
			return nil
		}
		return *val
	case *absser.UntypedDouble:
		val := v.GetValue()
		if val == nil {
			return nil
		}
		return *val
	case *absser.UntypedBoolean:
		val := v.GetValue()
		if val == nil {
			return nil
		}
		return *val
	default:
		// Fallback: round-trip through JSON serialization
		data, err := kjson.Marshal(node)
		if err != nil {
			return nil
		}
		var result interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil
		}
		return result
	}
}

// untypedToMap converts a Kiota UntypedNodeable to map[string]interface{}.
func untypedToMap(node absser.UntypedNodeable) map[string]interface{} {
	if node == nil {
		return map[string]interface{}{}
	}
	val := untypedToAny(node)
	if m, ok := val.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

// mergeUndeclaredFields lifts a typed model's overflow AdditionalData — the
// undeclared response properties the generated OpenAPI model does not name — up
// to the top level of out, so callers read them flat alongside the declared
// fields. This is what surfaces envelope fields the spec omits (e.g. identity_id
// when a given generation declares only status/results/warnings/routing_mode/
// request_class). Declared keys already present in out win; overflow only fills
// gaps. No-op when p carries no AdditionalData accessor.
func mergeUndeclaredFields(out map[string]interface{}, p absser.Parsable) {
	holder, ok := p.(interface{ GetAdditionalData() map[string]any })
	if !ok {
		return
	}
	for k, v := range holder.GetAdditionalData() {
		if _, exists := out[k]; exists {
			continue
		}
		// AdditionalData values arrive as Kiota untyped nodes; flatten them to
		// plain Go values so the merged key matches the shape of declared fields.
		if node, ok := v.(absser.UntypedNodeable); ok {
			out[k] = untypedToAny(node)
		} else {
			out[k] = v
		}
	}
}

// parsableToMap serializes a typed Kiota Parsable response to map[string]interface{} via JSON.
//
// NOTE: the Kiota JSON writer's GetSerializedContent() returns the object's field
// content WITHOUT the enclosing braces (e.g. `"email":"a","user_id":"b"` rather
// than `{"email":"a","user_id":"b"}`) — it is designed to be embedded by a parent
// writer. Feeding that brace-less fragment straight to json.Unmarshal fails, which
// previously made EVERY typed-response method (Me, Map, Extract, Webhooks.*) return
// an empty map on success. We wrap the fragment in braces before unmarshaling.
func parsableToMap(p absser.Parsable) map[string]interface{} {
	if p == nil {
		return map[string]interface{}{}
	}
	data, err := kjson.Marshal(p)
	if err != nil {
		return map[string]interface{}{}
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return map[string]interface{}{}
	}
	// Only wrap when the writer handed us a brace-less object fragment. A
	// already-braced object (or any other already-valid JSON object) is used as-is.
	if !strings.HasPrefix(trimmed, "{") {
		trimmed = "{" + trimmed + "}"
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return map[string]interface{}{}
	}
	return result
}

// ---------------------------------------------------------------------------
// Kiota core construction
// ---------------------------------------------------------------------------

// newKiotaCore creates the generated root client. Since client.go and ghostcrawl_client.go
// are both in package ghostcrawl (the module root), NewGhostCrawlClient (the generated
// constructor) is referenced without a package qualifier — same package, no import cycle.
func newKiotaCore(token, baseURL string) (*GhostCrawlClient, error) {
	authProvider := absauth.NewBaseBearerTokenAuthenticationProvider(
		newStaticTokenProvider(token),
	)
	// Build an HTTP client with request compression DISABLED.
	// The default kiota middleware pipeline enables gzip compression of request bodies,
	// but FastAPI does not decompress request bodies by default → 400.
	// We keep all other default middlewares (retry, redirect, user-agent, headers-inspection).
	compressionOff := khttp.NewCompressionOptions(false)
	middlewares, err := khttp.GetDefaultMiddlewaresWithOptions(&compressionOff)
	if err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to build middleware: %w", err)
	}
	httpClient := khttp.GetDefaultClient(middlewares...)
	adapter, err := khttp.NewNetHttpRequestAdapterWithParseNodeFactoryAndSerializationWriterFactoryAndHttpClient(
		authProvider, nil, nil, httpClient,
	)
	if err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to create request adapter: %w", err)
	}
	adapter.SetBaseUrl(baseURL)
	// NewGhostCrawlClient here is the Kiota-generated constructor in ghostcrawl_client.go,
	// not this file's exported New() function — both live in package ghostcrawl.
	return NewGhostCrawlClient(adapter), nil
}

// ---------------------------------------------------------------------------
// Request types (idiomatic facade parameters)
// ---------------------------------------------------------------------------

// ScrapeRequest holds parameters for Scrape().
type ScrapeRequest struct {
	URL               string
	Format            string
	Engine            string
	JavascriptEnabled *bool
	ExtractSchema     map[string]interface{}
	Extra             map[string]interface{}
}

// SearchRequest holds parameters for Search().
type SearchRequest struct {
	Query  string
	Engine string
	Limit  int
	// ProviderKey is your own search-backend API key (BYO; GhostCrawl charges no
	// markup). When set it is sent as the X-Provider-Authorization: Bearer
	// <ProviderKey> header the backend requires. Without it /v1/search replies
	// 401 search_backend_key_missing.
	ProviderKey string
	Extra       map[string]interface{}
}

// ExtractRequest holds parameters for Extract().
type ExtractRequest struct {
	URL    string
	Schema map[string]interface{}
	Extra  map[string]interface{}
}

// CrawlRequest holds parameters for Crawl().
type CrawlRequest struct {
	URL             string
	MaxDepth        int
	MaxPages        int
	IncludePatterns []string
	ExcludePatterns []string
	Extra           map[string]interface{}
}

// MapRequest holds parameters for Map().
type MapRequest struct {
	URL   string
	Extra map[string]interface{}
}

// PdfRequest holds parameters for Pdf().
type PdfRequest struct {
	URL string
	// PaperFormat is the page size: "a4" (default), "letter", "legal", "tabloid".
	PaperFormat string
	// Landscape renders in landscape orientation. Default false.
	Landscape bool
	// Engine is the browser engine. PDF is Chrome-only; "firefox" / "webkit" are
	// rejected with 400 pdf_engine_unsupported. Default "auto" (resolves to Chrome).
	Engine string
	Extra  map[string]interface{}
}

// ScreenshotRequest holds parameters for Screenshot().
type ScreenshotRequest struct {
	URL string
	// Format is the image format: "png" (default), "jpeg", "webp".
	Format string
	// FullPage captures the entire scrollable page rather than just the viewport.
	FullPage bool
	// ScreenshotSelector, when set, captures only the element matching this CSS selector.
	ScreenshotSelector string
	Extra              map[string]interface{}
}

// ContentRequest holds parameters for Content().
type ContentRequest struct {
	URL string
	// Engine is the browser engine ("auto" (default), "chrome", "firefox", "webkit").
	Engine string
	Extra  map[string]interface{}
}

// ---------------------------------------------------------------------------
// Sub-clients — each delegates to the generated v1 request builders
// ---------------------------------------------------------------------------

// CrawlRunsClient manages crawl runs — /v1/crawl-runs.
type CrawlRunsClient struct {
	v1      *genv1.V1RequestBuilder
	baseURL string
}

// StartCrawlRunRequest holds parameters for starting a crawl run.
type StartCrawlRunRequest struct {
	URL            string
	MaxDepth       int
	MaxPages       int
	FollowPatterns []string
	Extra          map[string]interface{}

	// WaitUntilComplete makes the START call itself block server-side until the
	// run reaches a terminal state (completed | failed | cancelled) or the
	// server-side window elapses — sending body `wait_until: "completed"`. When
	// set, Start returns the terminal run (results included on completed) in a
	// single round-trip; no client-side poll loop is needed. If the server
	// window elapses first the current NON-terminal run is returned (HTTP 200),
	// and the caller can hand run["run_id"] to WaitForCompletion to keep waiting.
	WaitUntilComplete bool
	// WaitTimeout bounds the server-side blocking window for WaitUntilComplete
	// (sent as `timeout_s`, rounded up to whole seconds). Zero uses the server
	// default (300s).
	WaitTimeout time.Duration
}

// Start starts a new crawl run from a seed URL.
// Delegates to POST /v1/crawl-runs via the generated CrawlRunsRequestBuilder.
func (c *CrawlRunsClient) Start(ctx context.Context, req StartCrawlRunRequest) (map[string]interface{}, error) {
	body := genv1.NewCrawlRunsPostRequestBody()
	// POST /v1/crawl-runs is the canonical StartAction contract (extra="forbid"):
	// it requires `seed_urls` (array) — NOT a singular `url` — and the `action`
	// discriminator. The single ergonomic URL maps to a one-element seed_urls list.
	data := map[string]any{
		"action":    "start",
		"seed_urls": []string{req.URL},
		"max_depth": req.MaxDepth,
		"max_pages": req.MaxPages,
	}
	// The StartAction body accepts `follow_patterns` (the only URL-filter field).
	// Any other field must be a real StartAction key — extra="forbid" rejects
	// unknown keys — so callers pass non-canonical fields via Extra at their own risk.
	if len(req.FollowPatterns) > 0 {
		data["follow_patterns"] = req.FollowPatterns
	}
	// Start-and-wait: the server blocks until the run is terminal (or its window
	// elapses) and returns the run inline. This is event-driven server-side —
	// there is no client poll loop.
	if req.WaitUntilComplete {
		data["wait_until"] = "completed"
		if req.WaitTimeout > 0 {
			data["timeout_s"] = secondsCeil(req.WaitTimeout)
		}
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.CrawlRuns().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	out := untypedToMap(result)
	if se := scanResultError(out); se != nil {
		return out, se
	}
	return out, nil
}

// List lists crawl runs.
// Delegates to GET /v1/crawl-runs via the generated CrawlRunsRequestBuilder.
func (c *CrawlRunsClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.CrawlRuns().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Get gets a single crawl run by ID.
// Delegates to GET /v1/crawl-runs/{run_id} via the generated builder.
func (c *CrawlRunsClient) Get(ctx context.Context, runID string) (map[string]interface{}, error) {
	result, err := c.v1.CrawlRuns().ByRun_id(runID).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Cancel cancels a running crawl run.
// Delegates to POST /v1/crawl-runs/{run_id}/cancel via the generated builder.
func (c *CrawlRunsClient) Cancel(ctx context.Context, runID string) (map[string]interface{}, error) {
	result, err := c.v1.CrawlRuns().ByRun_id(runID).Cancel().Post(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// defaultWaitWindow is the per-request server-side blocking window used by
// WaitForCompletion when the caller sets none. It matches the server default
// for `timeout_s` on GET /v1/crawl-runs/{run_id}?wait=true.
const defaultWaitWindow = 300 * time.Second

// isTerminalStatus reports whether a crawl-run status is final (no further
// progress possible). "canceled" is accepted alongside "cancelled" for the
// US/UK spelling either side may emit.
func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// secondsCeil rounds a duration up to whole seconds (never below 1), suitable
// for the integer `timeout_s` the API expects.
func secondsCeil(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		return 1
	}
	return s
}

// WaitOption configures WaitForCompletion.
type WaitOption func(*waitConfig)

type waitConfig struct {
	window time.Duration
}

// WithWaitWindow sets the per-request server-side blocking window (the
// `timeout_s` query parameter) that each long-poll iteration arms. The server
// holds the request open for up to this long before returning the current
// (possibly non-terminal) run, at which point WaitForCompletion re-arms. It does
// NOT bound the total wait — that is governed by the ctx deadline. Smaller
// windows re-issue more often; larger windows hold a single connection longer.
// Zero or negative leaves the default (300s / the server default).
func WithWaitWindow(d time.Duration) WaitOption {
	return func(c *waitConfig) {
		if d > 0 {
			c.window = d
		}
	}
}

// WaitForCompletion blocks until the crawl run reaches a terminal state
// (completed | failed | cancelled) and returns the final run, or returns
// ctx.Err() if ctx is cancelled or its deadline passes first.
//
// It is event-driven, not a sleep-poll: each iteration issues
// GET /v1/crawl-runs/{run_id}?wait=true&timeout_s=N, which the server holds open
// until the run is terminal or the window elapses. On a window timeout the API
// returns the current non-terminal run (HTTP 200) and this re-arms the next
// blocking window, honouring ctx throughout — the only waiting is the
// server-side block, and ctx cancellation interrupts even an in-flight window.
//
// Set the overall bound with a ctx deadline:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
//	defer cancel()
//	run, err := client.CrawlRuns().WaitForCompletion(ctx, runID)
//
// A terminal "failed"/"cancelled" run is returned as a value (nil error) — the
// wait succeeded in observing the outcome; inspect run["status"]. Transport and
// API errors (auth, rate-limit, 5xx) are returned as typed errors.
func (c *CrawlRunsClient) WaitForCompletion(ctx context.Context, runID string, opts ...WaitOption) (map[string]interface{}, error) {
	cfg := waitConfig{window: defaultWaitWindow}
	for _, o := range opts {
		o(&cfg)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Cap this window to the remaining ctx budget so the final blocking
		// request never over-runs the caller's deadline.
		window := cfg.window
		if dl, ok := ctx.Deadline(); ok {
			remaining := time.Until(dl)
			if remaining <= 0 {
				return nil, context.DeadlineExceeded
			}
			if remaining < window {
				window = remaining
			}
		}

		run, err := c.getWaiting(ctx, runID, secondsCeil(window))
		if err != nil {
			return nil, err
		}
		if status, _ := run["status"].(string); isTerminalStatus(status) {
			return run, nil
		}
		// Non-terminal: the server window elapsed. Loop re-arms the next
		// blocking window (subject to ctx) — no client-side sleep.
	}
}

// getWaiting issues the long-polling GET .../{run_id}?wait=true&timeout_s=N.
// The generated item builder has no query-parameter surface for this route, so
// the fully-qualified URL (with the wait params) is supplied via WithUrl, which
// still routes through the canonical adapter (auth + transport).
func (c *CrawlRunsClient) getWaiting(ctx context.Context, runID string, timeoutS int) (map[string]interface{}, error) {
	rawURL := fmt.Sprintf(
		"%s/v1/crawl-runs/%s?wait=true&timeout_s=%d",
		c.baseURL, url.PathEscape(runID), timeoutS,
	)
	result, err := c.v1.CrawlRuns().ByRun_id(runID).WithUrl(rawURL).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// SessionsClient manages browser sessions — /v1/sessions.
type SessionsClient struct{ v1 *genv1.V1RequestBuilder }

// CreateSessionRequest holds parameters for creating a session.
type CreateSessionRequest struct {
	ProfileName string
	Extra       map[string]interface{}
}

// List lists all active sessions.
// Delegates to GET /v1/sessions via the generated SessionsRequestBuilder.
func (c *SessionsClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Sessions().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Create creates a new browser session.
// Delegates to POST /v1/sessions/create via the generated builder.
func (c *SessionsClient) Create(ctx context.Context, req CreateSessionRequest) (map[string]interface{}, error) {
	body := genmodels.NewSessionCreateRequest()
	// Clear typed default (engine=chrome) — avoid duplicate key with additionalData.
	body.SetEngine(nil)
	data := map[string]any{"profile": req.ProfileName}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Sessions().Create().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Extend extends a session's TTL.
// Delegates to POST /v1/sessions/{id}/extend via the generated builder.
// durationSeconds is passed as ttl_seconds in the request body.
func (c *SessionsClient) Extend(ctx context.Context, sessionID string, durationSeconds int) (map[string]interface{}, error) {
	body := genmodels.NewExtendBody()
	body.SetAdditionalData(map[string]any{"ttl_seconds": durationSeconds})
	result, err := c.v1.Sessions().ByProfile_Id(sessionID).Extend().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Release releases a session back to the pool.
// Delegates to POST /v1/sessions/{id}/release via the generated builder.
func (c *SessionsClient) Release(ctx context.Context, sessionID string) (map[string]interface{}, error) {
	result, err := c.v1.Sessions().ByProfile_Id(sessionID).Release().Post(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// ProfilesClient manages identity profiles — /v1/profiles.
type ProfilesClient struct{ v1 *genv1.V1RequestBuilder }

// List lists all profiles.
// Delegates to GET /v1/profiles via the generated ProfilesRequestBuilder.
func (c *ProfilesClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Profiles().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Get gets a profile by name.
// Delegates to GET /v1/profiles/{name} via the generated builder.
func (c *ProfilesClient) Get(ctx context.Context, name string) (map[string]interface{}, error) {
	result, err := c.v1.Profiles().ByName(name).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Create creates a new profile.
// Delegates to POST /v1/profiles via the generated ProfilesRequestBuilder.
func (c *ProfilesClient) Create(ctx context.Context, name string, config map[string]interface{}) (map[string]interface{}, error) {
	body := genmodels.NewProfileCreateRequest()
	data := map[string]any{"name": name}
	for k, v := range config {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Profiles().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Update updates a profile.
// Delegates to PUT /v1/profiles/{name} via the generated builder.
func (c *ProfilesClient) Update(ctx context.Context, name string, config map[string]interface{}) (map[string]interface{}, error) {
	body := genmodels.NewProfileUpdateRequest()
	body.SetAdditionalData(toAnyMap(config))
	result, err := c.v1.Profiles().ByName(name).Put(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Delete deletes a profile.
// Delegates to DELETE /v1/profiles/{name} via the generated builder.
func (c *ProfilesClient) Delete(ctx context.Context, name string) (map[string]interface{}, error) {
	result, err := c.v1.Profiles().ByName(name).Delete(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// WebhooksClient manages webhooks — /v1/webhooks.
type WebhooksClient struct{ v1 *genv1.V1RequestBuilder }

// CreateWebhookRequest holds parameters for creating a webhook.
type CreateWebhookRequest struct {
	URL    string
	Events []string
	Extra  map[string]interface{}
}

// List lists all webhooks.
// Delegates to GET /v1/webhooks via the generated WebhooksRequestBuilder.
// Returns WebhookListResponseable as a map via parsableToMap.
func (c *WebhooksClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Webhooks().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Get gets a webhook by ID.
// Delegates to GET /v1/webhooks/{id} via the generated builder.
// Returns WebhookPublicable as a map via parsableToMap.
func (c *WebhooksClient) Get(ctx context.Context, webhookID string) (map[string]interface{}, error) {
	result, err := c.v1.Webhooks().ByWebhook_id(webhookID).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Create registers a new webhook endpoint.
// Delegates to POST /v1/webhooks via the generated WebhooksRequestBuilder.
// Returns WebhookCreateResponseable as a map via parsableToMap.
func (c *WebhooksClient) Create(ctx context.Context, req CreateWebhookRequest) (map[string]interface{}, error) {
	body := genmodels.NewWebhookCreateRequest()
	data := map[string]any{"url": req.URL}
	if len(req.Events) > 0 {
		// The API field is "event_types".
		data["event_types"] = req.Events
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Webhooks().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Delete deletes a webhook.
// Delegates to DELETE /v1/webhooks/{id} via the generated builder.
func (c *WebhooksClient) Delete(ctx context.Context, webhookID string) (map[string]interface{}, error) {
	if err := c.v1.Webhooks().ByWebhook_id(webhookID).Delete(ctx, nil); err != nil {
		return nil, wrapAPIError(err)
	}
	return map[string]interface{}{}, nil
}

// RotateSecret rotates the signing secret for a webhook.
// Delegates to POST /v1/webhooks/{id}/rotate-secret via the generated builder.
// Returns RotateSecretResponseable as a map via parsableToMap.
func (c *WebhooksClient) RotateSecret(ctx context.Context, webhookID string) (map[string]interface{}, error) {
	result, err := c.v1.Webhooks().ByWebhook_id(webhookID).RotateSecret().Post(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// SchedulesClient manages schedules — /v1/schedules.
type SchedulesClient struct{ v1 *genv1.V1RequestBuilder }

// CreateScheduleRequest holds parameters for creating a schedule.
//
// The API contract for POST /v1/schedules requires `name`, `job_type`
// ('scrape' | 'crawl' | 'change_monitor'), `cron_expr` (5-field cron), and
// `job_params` (the full scrape/crawl request body). This struct offers an
// ergonomic {Cron, Task} form that is mapped onto that contract:
//   - Cron            → cron_expr
//   - Task["action"] (or Task["job_type"]) → job_type; the remaining Task keys
//     become job_params (or pass Task["job_params"] explicitly)
//   - Name            → required; auto-generated when empty
//
// Explicit contract fields (Name, JobType, JobParams) take precedence over the
// values derived from Task.
type CreateScheduleRequest struct {
	Cron      string
	Task      map[string]interface{}
	Name      string
	JobType   string
	JobParams map[string]interface{}
	Extra     map[string]interface{}
}

// List lists all schedules.
// Delegates to GET /v1/schedules via the generated SchedulesRequestBuilder.
func (c *SchedulesClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Schedules().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Get gets a schedule by ID.
// Delegates to GET /v1/schedules/{id} via the generated builder.
func (c *SchedulesClient) Get(ctx context.Context, scheduleID string) (map[string]interface{}, error) {
	result, err := c.v1.Schedules().BySchedule_id(scheduleID).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Create creates a new schedule.
// Delegates to POST /v1/schedules via the generated SchedulesRequestBuilder.
func (c *SchedulesClient) Create(ctx context.Context, req CreateScheduleRequest) (map[string]interface{}, error) {
	body := genmodels.NewScheduleCreateRequest()
	// Clear typed default (monitor_mode=false) — avoid duplicate key with additionalData.
	body.SetMonitorMode(nil)

	// Derive job_type + job_params from the ergonomic Task map (Node-parity),
	// then let explicit contract fields (JobType/JobParams/Name) win.
	task := req.Task
	if task == nil {
		task = map[string]interface{}{}
	}
	jobType := req.JobType
	if jobType == "" {
		if a, ok := task["action"].(string); ok {
			jobType = a
		} else if jt, ok := task["job_type"].(string); ok {
			jobType = jt
		}
	}
	var jobParams map[string]interface{}
	if req.JobParams != nil {
		jobParams = req.JobParams
	} else if jp, ok := task["job_params"].(map[string]interface{}); ok {
		jobParams = jp
	} else {
		jobParams = map[string]interface{}{}
		for k, v := range task {
			if k == "action" || k == "job_type" || k == "job_params" {
				continue
			}
			jobParams[k] = v
		}
	}
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("schedule-%d", time.Now().UnixMilli())
	}

	// Use the generated typed setters so the required contract fields serialize
	// under their correct keys (name, cron_expr, job_type, job_params).
	body.SetName(&name)
	body.SetCronExpr(&req.Cron)
	if jobType != "" {
		body.SetJobType(&jobType)
	}
	jp := genmodels.NewScheduleCreateRequest_job_params()
	jp.SetAdditionalData(jobParams)
	body.SetJobParams(jp)

	// Any additional/override fields go through AdditionalData.
	if len(req.Extra) > 0 {
		data := map[string]any{}
		for k, v := range req.Extra {
			data[k] = v
		}
		body.SetAdditionalData(data)
	}

	result, err := c.v1.Schedules().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Delete deletes a schedule.
// Delegates to DELETE /v1/schedules/{id} via the generated builder.
func (c *SchedulesClient) Delete(ctx context.Context, scheduleID string) (map[string]interface{}, error) {
	result, err := c.v1.Schedules().BySchedule_id(scheduleID).Delete(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// DatasetsClient manages datasets — /v1/datasets.
type DatasetsClient struct{ v1 *genv1.V1RequestBuilder }

// List lists all datasets.
// Delegates to GET /v1/datasets via the generated DatasetsRequestBuilder.
func (c *DatasetsClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Datasets().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Get gets a dataset by name.
// Delegates to GET /v1/datasets/{name} via the generated builder.
func (c *DatasetsClient) Get(ctx context.Context, name string) (map[string]interface{}, error) {
	result, err := c.v1.Datasets().ByName(name).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Create creates a new dataset.
// Delegates to POST /v1/datasets via the generated DatasetsRequestBuilder.
func (c *DatasetsClient) Create(ctx context.Context, name string) (map[string]interface{}, error) {
	body := genv1.NewDatasetsPostRequestBody()
	body.SetAdditionalData(map[string]any{"name": name})
	result, err := c.v1.Datasets().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Delete deletes a dataset.
// Delegates to DELETE /v1/datasets/{name} via the generated builder.
func (c *DatasetsClient) Delete(ctx context.Context, name string) (map[string]interface{}, error) {
	result, err := c.v1.Datasets().ByName(name).Delete(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Rows gets rows from a dataset.
// Delegates to GET /v1/datasets/{name}/rows via the generated builder.
func (c *DatasetsClient) Rows(ctx context.Context, name string) (map[string]interface{}, error) {
	result, err := c.v1.Datasets().ByName(name).Rows().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Append appends rows to a dataset.
// Delegates to POST /v1/datasets/{name}/rows/append via the generated builder.
func (c *DatasetsClient) Append(ctx context.Context, name string, rows []interface{}) (map[string]interface{}, error) {
	body := genv1.NewDatasetsItemRowsAppendPostRequestBody()
	body.SetAdditionalData(map[string]any{"rows": rows})
	result, err := c.v1.Datasets().ByName(name).Rows().Append().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// RecordingsClient manages session recordings — /v1/recordings.
type RecordingsClient struct{ v1 *genv1.V1RequestBuilder }

// List lists all recordings.
// Delegates to GET /v1/recordings via the generated RecordingsRequestBuilder.
func (c *RecordingsClient) List(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Recordings().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Get gets a recording by ID.
// Delegates to GET /v1/recordings/{id} via the generated builder.
func (c *RecordingsClient) Get(ctx context.Context, recordingID string) (map[string]interface{}, error) {
	result, err := c.v1.Recordings().ByRecording_Id(recordingID).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Delete deletes a recording.
// Delegates to DELETE /v1/recordings/{id} via the generated builder.
func (c *RecordingsClient) Delete(ctx context.Context, recordingID string) (map[string]interface{}, error) {
	if err := c.v1.Recordings().ByRecording_Id(recordingID).Delete(ctx, nil); err != nil {
		return nil, wrapAPIError(err)
	}
	return map[string]interface{}{}, nil
}

// KVClient provides access to the key-value store — /v1/kv.
type KVClient struct{ v1 *genv1.V1RequestBuilder }

// Get gets a value by key.
// Delegates to GET /v1/kv/{key} via the generated KvRequestBuilder.
func (c *KVClient) Get(ctx context.Context, key string) (map[string]interface{}, error) {
	result, err := c.v1.Kv().ByKey(key).Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Set sets a key-value pair.
// Delegates to PUT /v1/kv/{key} via the generated builder.
func (c *KVClient) Set(ctx context.Context, key string, value interface{}) (map[string]interface{}, error) {
	body := genv1.NewKvItemWithKeyPutRequestBody()
	body.SetAdditionalData(map[string]any{"value": value})
	result, err := c.v1.Kv().ByKey(key).Put(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Delete deletes a key.
// Delegates to DELETE /v1/kv/{key} via the generated builder.
func (c *KVClient) Delete(ctx context.Context, key string) (map[string]interface{}, error) {
	result, err := c.v1.Kv().ByKey(key).Delete(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// toAnyMap converts map[string]interface{} to map[string]any (same underlying type).
func toAnyMap(m map[string]interface{}) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// ---------------------------------------------------------------------------
// Main facade — Client
// ---------------------------------------------------------------------------

// Client is the GhostCrawl idiomatic API client.
//
// Delegates all HTTP transport, URL routing, serialization, and auth to the
// Kiota-generated canonical core (_generated/). This facade is the shipped API.
//
// Create with New:
//
//	client, err := ghostcrawl.New("gck_live_YOUR_KEY")
//	result, err := client.Scrape(ctx, ghostcrawl.ScrapeRequest{URL: "https://example.com"})
type Client struct {
	core       *GhostCrawlClient
	v1         *genv1.V1RequestBuilder
	adapter    abstractions.RequestAdapter
	baseURL    string
	crawlRuns  *CrawlRunsClient
	sessions   *SessionsClient
	profiles   *ProfilesClient
	webhooks   *WebhooksClient
	schedules  *SchedulesClient
	datasets   *DatasetsClient
	recordings *RecordingsClient
	kv         *KVClient
}

// New creates a new GhostCrawl API client.
//
// Delegates to the Kiota-generated core via BaseBearerTokenAuthenticationProvider
// and NetHttpRequestAdapter.
//
// If token is empty, the client reads GHOSTCRAWL_API_KEY from the environment.
// If baseURL is empty, reads GHOSTCRAWL_BASE_URL, falling back to https://api.ghostcrawl.io.
func New(token string, baseURL ...string) (*Client, error) {
	resolvedToken := token
	if resolvedToken == "" {
		resolvedToken = os.Getenv("GHOSTCRAWL_API_KEY")
	}
	if resolvedToken == "" {
		return nil, fmt.Errorf("ghostcrawl: token is required — pass it to New or set GHOSTCRAWL_API_KEY. Get your key at https://ghostcrawl.io")
	}

	resolvedBase := defaultBaseURL
	if len(baseURL) > 0 && baseURL[0] != "" {
		resolvedBase = baseURL[0]
	} else if envBase := os.Getenv("GHOSTCRAWL_BASE_URL"); envBase != "" {
		resolvedBase = envBase
	}
	resolvedBase = strings.TrimRight(resolvedBase, "/")

	core, err := newKiotaCore(resolvedToken, resolvedBase)
	if err != nil {
		return nil, err
	}

	v1 := core.V1()
	return &Client{
		core:       core,
		v1:         v1,
		adapter:    core.BaseRequestBuilder.RequestAdapter,
		baseURL:    resolvedBase,
		crawlRuns:  &CrawlRunsClient{v1: v1, baseURL: resolvedBase},
		sessions:   &SessionsClient{v1: v1},
		profiles:   &ProfilesClient{v1: v1},
		webhooks:   &WebhooksClient{v1: v1},
		schedules:  &SchedulesClient{v1: v1},
		datasets:   &DatasetsClient{v1: v1},
		recordings: &RecordingsClient{v1: v1},
		kv:         &KVClient{v1: v1},
	}, nil
}

// Sub-client accessors

// CrawlRuns returns the crawl run management sub-client.
func (c *Client) CrawlRuns() *CrawlRunsClient { return c.crawlRuns }

// Sessions returns the browser session management sub-client.
func (c *Client) Sessions() *SessionsClient { return c.sessions }

// Profiles returns the identity profile management sub-client.
func (c *Client) Profiles() *ProfilesClient { return c.profiles }

// Webhooks returns the webhook management sub-client.
func (c *Client) Webhooks() *WebhooksClient { return c.webhooks }

// Schedules returns the schedule management sub-client.
func (c *Client) Schedules() *SchedulesClient { return c.schedules }

// Datasets returns the dataset management sub-client.
func (c *Client) Datasets() *DatasetsClient { return c.datasets }

// Recordings returns the recording management sub-client.
func (c *Client) Recordings() *RecordingsClient { return c.recordings }

// KV returns the key-value store sub-client.
func (c *Client) KV() *KVClient { return c.kv }

// ---------------------------------------------------------------------------
// Top-level facade methods — delegate to generated builders
// ---------------------------------------------------------------------------

// Scrape scrapes a single URL and returns the rendered content.
// Delegates to POST /v1/scrape via the generated ScrapeRequestBuilder.
func (c *Client) Scrape(ctx context.Context, req ScrapeRequest) (map[string]interface{}, error) {
	if req.Format == "" {
		req.Format = "markdown"
	}
	if req.Engine == "" {
		req.Engine = "auto"
	}
	body := genmodels.NewScrapeRequest()
	// Clear all typed defaults from NewScrapeRequest() — these would produce duplicate
	// keys in the serialized JSON alongside our additionalData values, causing 400s.
	body.SetBatchIdentityMode(nil)
	body.SetChunkTokens(nil)
	body.SetEngine(nil)
	body.SetFormat(nil)
	body.SetFullPage(nil)
	body.SetIncludeCitations(nil)
	body.SetScreenshot(nil)
	body.SetStream(nil)
	data := map[string]any{
		"url":    req.URL,
		"format": req.Format,
		"engine": req.Engine,
	}
	if req.JavascriptEnabled != nil {
		data["javascript_enabled"] = *req.JavascriptEnabled
	} else {
		data["javascript_enabled"] = true
	}
	if req.ExtractSchema != nil {
		data["extract_schema"] = req.ExtractSchema
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Scrape().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	// A 200 response can still carry a TARGET failure (blocked / captcha /
	// target_http_error / …). Surface it as a *ScrapeError instead of returning
	// a "successful" but unusable result.
	out := parsableToMap(result)
	// Lift any undeclared envelope fields (e.g. identity_id when the spec omits
	// it from the model) from the overflow bucket up to the top level so they are
	// readable flat, not buried in an AdditionalData sub-map.
	mergeUndeclaredFields(out, result)
	// The API returns the rendered page under a format-specific key
	// ("markdown"/"html"/"text"). Mirror it to a stable "content" key so callers
	// (and the README quickstart) can read result["content"] regardless of format.
	normalizeScrapeContent(out)
	if se := scanResultError(out); se != nil {
		return out, se
	}
	return out, nil
}

// Search searches the web and returns results.
// Delegates to POST /v1/search via the generated SearchRequestBuilder.
func (c *Client) Search(ctx context.Context, req SearchRequest) (map[string]interface{}, error) {
	if req.Engine == "" {
		req.Engine = "google"
	}
	if req.Limit == 0 {
		req.Limit = 10
	}
	body := genmodels.NewSearchRequest()
	// Clear typed default (limit=10) — avoid duplicate key with additionalData.
	body.SetLimit(nil)
	data := map[string]any{
		"query":  req.Query,
		"engine": req.Engine,
		"limit":  req.Limit,
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	var config *genv1.SearchRequestBuilderPostRequestConfiguration
	if req.ProviderKey != "" {
		headers := abstractions.NewRequestHeaders()
		headers.TryAdd("X-Provider-Authorization", "Bearer "+req.ProviderKey)
		config = &genv1.SearchRequestBuilderPostRequestConfiguration{Headers: headers}
	}
	result, err := c.v1.Search().Post(ctx, body, config)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return untypedToMap(result), nil
}

// Extract extracts structured data from a URL using a JSON Schema.
// Delegates to POST /v1/extract via the generated ExtractRequestBuilder.
// Returns ExtractResponseable as a map via parsableToMap.
func (c *Client) Extract(ctx context.Context, req ExtractRequest) (map[string]interface{}, error) {
	body := genmodels.NewExtractRequest()
	// Clear typed default (engine=auto) — avoid duplicate key with additionalData.
	body.SetEngine(nil)
	data := map[string]any{
		"url":    req.URL,
		"schema": req.Schema,
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Extract().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	out := parsableToMap(result)
	if se := scanResultError(out); se != nil {
		return out, se
	}
	return out, nil
}

// Crawl starts a deep crawl from a seed URL.
// Delegates to POST /v1/crawl/deep via the generated CrawlDeepRequestBuilder.
func (c *Client) Crawl(ctx context.Context, req CrawlRequest) (map[string]interface{}, error) {
	if req.MaxDepth == 0 {
		req.MaxDepth = 2
	}
	if req.MaxPages == 0 {
		req.MaxPages = 100
	}
	body := genmodels.NewDeepCrawlBody()
	// Clear typed defaults — avoid duplicate keys alongside additionalData values.
	body.SetIncludeSitemaps(nil)
	body.SetMaxDepth(nil)
	body.SetMaxUrls(nil)
	body.SetRespectRobots(nil)
	body.SetStrategy(nil)
	body.SetStream(nil)
	data := map[string]any{
		"seed_urls": []string{req.URL},
		"max_depth": req.MaxDepth,
		"max_urls":  req.MaxPages,
	}
	if len(req.IncludePatterns) > 0 {
		data["include_patterns"] = req.IncludePatterns
	}
	if len(req.ExcludePatterns) > 0 {
		data["exclude_patterns"] = req.ExcludePatterns
	}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.Crawl().Deep().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	out := untypedToMap(result)
	if se := scanResultError(out); se != nil {
		return out, se
	}
	return out, nil
}

// Map maps all URLs reachable from a seed URL.
// Delegates to POST /v1/map via the generated MapRequestBuilder (accessed via MapEscaped()).
// Returns MapResponseable as a map via parsableToMap.
func (c *Client) Map(ctx context.Context, req MapRequest) (map[string]interface{}, error) {
	body := genmodels.NewMapBody()
	// Clear typed defaults — avoid duplicate keys alongside additionalData values.
	body.SetIgnoreSitemap(nil)
	body.SetIncludeSubdomains(nil)
	body.SetLimit(nil)
	data := map[string]any{"url": req.URL}
	for k, v := range req.Extra {
		data[k] = v
	}
	body.SetAdditionalData(data)
	result, err := c.v1.MapEscaped().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Pdf renders a URL to a PDF document and returns the raw application/pdf bytes.
// Delegates to POST /v1/pdf.
//
// Unlike the JSON endpoints, /v1/pdf responds with binary application/pdf, so
// this returns the bytes verbatim (ready to write to disk) rather than a decoded
// map:
//
//	data, err := client.Pdf(ctx, ghostcrawl.PdfRequest{URL: "https://example.com"})
//	if err != nil { log.Fatal(err) }
//	os.WriteFile("page.pdf", data, 0o644)
//
// PDF output is Chrome-only; a request that resolves to a Firefox or WebKit
// identity is rejected with a *ProblemError (StatusCode 400 pdf_engine_unsupported).
//
// /v1/pdf has no generated typed builder, so this uses the shared Kiota request
// adapter (same Bearer token + base URL + middleware pipeline as the modeled calls).
func (c *Client) Pdf(ctx context.Context, req PdfRequest) ([]byte, error) {
	paperFormat := req.PaperFormat
	if paperFormat == "" {
		paperFormat = "a4"
	}
	engine := req.Engine
	if engine == "" {
		engine = "auto"
	}
	data := map[string]interface{}{
		"url":          req.URL,
		"paper_format": paperFormat,
		"landscape":    req.Landscape,
		"engine":       engine,
	}
	for k, v := range req.Extra {
		data[k] = v
	}

	ri := abstractions.NewRequestInformation()
	ri.Method = abstractions.POST
	// A URL template with no expressions is treated as a literal URL by Kiota.
	ri.UrlTemplate = c.baseURL + "/v1/pdf"
	ri.Headers.TryAdd("Accept", "application/pdf")
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to encode request body: %w", err)
	}
	ri.SetStreamContentAndContentType(payload, "application/json")

	raw, err := c.adapter.SendPrimitive(ctx, ri, "[]byte", nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if raw == nil {
		return []byte{}, nil
	}
	out, ok := raw.([]byte)
	if !ok {
		return []byte{}, nil
	}
	return out, nil
}

// Screenshot captures a URL as an image and returns the raw image bytes.
// Delegates to POST /v1/screenshot.
//
// Like /v1/pdf, /v1/screenshot responds with binary (image/png by default), so
// this returns the bytes verbatim (ready to write to disk) rather than a decoded
// map:
//
//	data, err := client.Screenshot(ctx, ghostcrawl.ScreenshotRequest{URL: "https://example.com"})
//	if err != nil { log.Fatal(err) }
//	os.WriteFile("page.png", data, 0o644)
//
// /v1/screenshot has no generated typed builder, so this uses the shared Kiota
// request adapter (same Bearer token + base URL + middleware pipeline as the
// modeled calls), mirroring Pdf().
func (c *Client) Screenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	format := req.Format
	if format == "" {
		format = "png"
	}
	data := map[string]interface{}{
		"url":       req.URL,
		"format":    format,
		"full_page": req.FullPage,
	}
	if req.ScreenshotSelector != "" {
		data["screenshot_selector"] = req.ScreenshotSelector
	}
	for k, v := range req.Extra {
		data[k] = v
	}

	ri := abstractions.NewRequestInformation()
	ri.Method = abstractions.POST
	// A URL template with no expressions is treated as a literal URL by Kiota.
	ri.UrlTemplate = c.baseURL + "/v1/screenshot"
	ri.Headers.TryAdd("Accept", "image/png")
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to encode request body: %w", err)
	}
	ri.SetStreamContentAndContentType(payload, "application/json")

	raw, err := c.adapter.SendPrimitive(ctx, ri, "[]byte", nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if raw == nil {
		return []byte{}, nil
	}
	out, ok := raw.([]byte)
	if !ok {
		return []byte{}, nil
	}
	return out, nil
}

// Content renders a URL and returns the rendered-content envelope as a map.
// Delegates to POST /v1/content (Accept: application/json), returning the decoded
// JSON body ({url, status, format, status_code, content, bytes, ...}).
//
// /v1/content has no generated typed builder, so this uses the shared Kiota
// request adapter (same Bearer token + base URL + middleware pipeline as the
// modeled calls), mirroring Pdf() with a JSON Accept + decode.
func (c *Client) Content(ctx context.Context, req ContentRequest) (map[string]interface{}, error) {
	engine := req.Engine
	if engine == "" {
		engine = "auto"
	}
	data := map[string]interface{}{
		"url":    req.URL,
		"engine": engine,
	}
	for k, v := range req.Extra {
		data[k] = v
	}

	ri := abstractions.NewRequestInformation()
	ri.Method = abstractions.POST
	// A URL template with no expressions is treated as a literal URL by Kiota.
	ri.UrlTemplate = c.baseURL + "/v1/content"
	ri.Headers.TryAdd("Accept", "application/json")
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to encode request body: %w", err)
	}
	ri.SetStreamContentAndContentType(payload, "application/json")

	raw, err := c.adapter.SendPrimitive(ctx, ri, "[]byte", nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if raw == nil {
		return map[string]interface{}{}, nil
	}
	rawBytes, ok := raw.([]byte)
	if !ok {
		return map[string]interface{}{}, nil
	}
	trimmed := strings.TrimSpace(string(rawBytes))
	if trimmed == "" {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return nil, fmt.Errorf("ghostcrawl: failed to decode response: %w", err)
	}
	return result, nil
}

// Me gets the current account's profile.
// Delegates to GET /v1/me via the generated MeRequestBuilder.
// Returns MeResponseable as a map via parsableToMap.
func (c *Client) Me(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Me().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Whoami gets the current account's identity — an alias of Me.
// Delegates to GET /v1/me. Mirrors the Python SDK's whoami() and the Node SDK's
// whoami() (parity across SDKs).
func (c *Client) Whoami(ctx context.Context) (map[string]interface{}, error) {
	return c.Me(ctx)
}

// Usage gets the current account's cost/usage report.
// Delegates to GET /v1/me/usage via the generated me.usage sub-builder. Parity
// with the Python SDK's usage() and the Node SDK's usage().
func (c *Client) Usage(ctx context.Context) (map[string]interface{}, error) {
	result, err := c.v1.Me().Usage().Get(ctx, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// Identity materialises a fresh browser identity envelope.
// Delegates to POST /v1/identity via the generated IdentityRequestBuilder. All
// options are optional; supply claim_os / claim_browser / device_model /
// viewport / locale / timezone / proxy / persist to constrain the draw. Parity
// with the Python SDK's identity() and the Node SDK's identity().
func (c *Client) Identity(ctx context.Context, options map[string]interface{}) (map[string]interface{}, error) {
	body := genmodels.NewIdentityRequest()
	if len(options) > 0 {
		body.SetAdditionalData(toAnyMap(options))
	}
	result, err := c.v1.Identity().Post(ctx, body, nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	return parsableToMap(result), nil
}

// IdentityDevices lists the device models available per claim_os for identity
// materialisation. Delegates to GET /v1/identity/devices. Parity with the Python
// SDK's identity_devices() and the Node SDK's identityDevices(). Returns
// { ios: [...], android: [...], ... }.
//
// /v1/identity/devices has no generated request builder, so this uses the generic
// authenticated request path (same Bearer token + base URL as the modeled calls).
func (c *Client) IdentityDevices(ctx context.Context) (map[string]interface{}, error) {
	return c.request(ctx, abstractions.GET, "/v1/identity/devices", nil)
}

// Agent runs an autonomous agent task. Delegates to POST /v1/agent. Parity with
// the Python SDK's agent() and the Node SDK's agent(). Note: the agent endpoint
// may be unavailable (404) on a given deployment; callers should handle that
// status (a *ProblemError with StatusCode 404).
//
// /v1/agent has no generated request builder, so this uses the generic
// authenticated request path.
func (c *Client) Agent(ctx context.Context, options map[string]interface{}) (map[string]interface{}, error) {
	return c.request(ctx, abstractions.POST, "/v1/agent", options)
}

// request is the generic authenticated request escape hatch for endpoints that
// have no generated typed builder (currently /v1/identity/devices and /v1/agent).
// It applies the same Bearer token, base URL, and middleware pipeline as the
// modeled calls via the shared Kiota request adapter, and returns the parsed JSON
// body as a map. Non-2xx responses are surfaced as the same typed errors the
// modeled methods raise (via wrapAPIError).
func (c *Client) request(ctx context.Context, method abstractions.HttpMethod, path string, body map[string]interface{}) (map[string]interface{}, error) {
	ri := abstractions.NewRequestInformation()
	ri.Method = method
	// A URL template with no expressions is treated as a literal URL by Kiota.
	ri.UrlTemplate = c.baseURL + path
	ri.Headers.TryAdd("Accept", "application/json")
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("ghostcrawl: failed to encode request body: %w", err)
		}
		ri.SetStreamContentAndContentType(payload, "application/json")
	}
	raw, err := c.adapter.SendPrimitive(ctx, ri, "[]byte", nil)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if raw == nil {
		return map[string]interface{}{}, nil
	}
	data, ok := raw.([]byte)
	if !ok {
		return map[string]interface{}{}, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		// Non-object JSON (e.g. an array) — wrap under a "data" key so callers get
		// a stable map rather than an error.
		var any interface{}
		if err2 := json.Unmarshal([]byte(trimmed), &any); err2 == nil {
			return map[string]interface{}{"data": any}, nil
		}
		return nil, fmt.Errorf("ghostcrawl: failed to decode response: %w", err)
	}
	return result, nil
}
