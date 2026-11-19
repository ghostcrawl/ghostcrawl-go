package ghostcrawl

import (
	"errors"
	"testing"

	genmodels "github.com/GhostCrawl/ghostcrawl-go/v2/models"
)

// wrapAPIError must carry the canonical fields from a modeled *ProblemDetails
// straight through — Code, RequestID (instance), Retryable, and RetryAfter — not
// collapse them to a status-line fallback. This is the regression the battle-test
// found: RequestID was always "" and every 503 subtype flattened to a generic
// error because ae.Error() is only the generic Kiota status message.
func TestWrapAPIError_CarriesProblemDetailsThrough(t *testing.T) {
	pd := genmodels.NewProblemDetails()
	pd.SetStatusCode(503)
	code := genmodels.POOL_EXHAUSTED_ERRORCODE
	pd.SetCode(&code)
	inst := "req_model_xyz"
	pd.SetInstance(&inst)
	ra := int32(7)
	pd.SetRetryAfter(&ra)

	err := wrapAPIError(pd)
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("503 ProblemDetails should map to *APIError, got %T", err)
	}
	if ae.Code != CodePoolExhausted {
		t.Errorf("Code = %q, want pool_exhausted (from the modeled body, not the status fallback)", ae.Code)
	}
	if ae.RequestID != "req_model_xyz" {
		t.Errorf("RequestID = %q, want req_model_xyz (must carry through, not be discarded)", ae.RequestID)
	}
	if !ae.Retryable {
		t.Errorf("Retryable = false, want true")
	}
	if ae.RetryAfter != 7 {
		t.Errorf("RetryAfter = %d, want 7", ae.RetryAfter)
	}
}

// A ProblemDetails whose modeled code disagrees with the status line must honour
// the modeled code — proving the model path is preferred over codeForStatus.
func TestWrapAPIError_ModeledCodeWins(t *testing.T) {
	pd := genmodels.NewProblemDetails()
	pd.SetStatusCode(429)
	code := genmodels.RATE_LIMITED_ERRORCODE
	pd.SetCode(&code)
	err := wrapAPIError(pd)
	rl, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("429 should map to *RateLimitError, got %T", err)
	}
	if rl.Code != CodeRateLimited || !rl.Retryable {
		t.Errorf("Code=%q retryable=%v, want rate_limited/true", rl.Code, rl.Retryable)
	}
}

// A connection-level failure (never reached the API) must surface as the typed
// *TransportError family, unwrap to the underlying cause, and be marked retryable.
func TestWrapAPIError_TransportErrorTyped(t *testing.T) {
	cause := errors.New("dial tcp 127.0.0.1:0: connect: connection refused")
	err := wrapAPIError(cause)
	te, ok := err.(*TransportError)
	if !ok {
		t.Fatalf("connection failure should map to *TransportError, got %T", err)
	}
	if te.StatusCode != 0 || te.Code != "" {
		t.Errorf("transport error should have no status/code, got status=%d code=%q", te.StatusCode, te.Code)
	}
	if !te.Retryable {
		t.Errorf("Retryable = false, want true (transport failures are transient)")
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false, want true (must unwrap to the underlying error)")
	}
}

// A 200 result carrying ok:false + result_error must raise a *ScrapeError with
// the canonical code, retryable flag, and target_status.
func TestScanResultError_TargetHTTPError(t *testing.T) {
	result := map[string]interface{}{
		"ok": false,
		"result_error": map[string]interface{}{
			"code":          "target_http_error",
			"retryable":     false,
			"target_status": float64(404), // JSON numbers decode to float64
		},
		"request_id": "req_abc",
	}
	se := scanResultError(result)
	if se == nil {
		t.Fatal("expected a *ScrapeError for ok:false result, got nil")
	}
	if se.Code != CodeTargetHTTPError {
		t.Errorf("Code = %q, want %q", se.Code, CodeTargetHTTPError)
	}
	if se.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if se.TargetStatus != 404 {
		t.Errorf("TargetStatus = %d, want 404", se.TargetStatus)
	}
	if se.RequestID != "req_abc" {
		t.Errorf("RequestID = %q, want req_abc", se.RequestID)
	}
}

// "blocked" is retryable (rotate identity/proxy) per the catalog.
func TestScanResultError_BlockedRetryable(t *testing.T) {
	se := scanResultError(map[string]interface{}{"ok": false, "code": "blocked"})
	if se == nil {
		t.Fatal("expected a *ScrapeError")
	}
	if se.Code != CodeBlocked {
		t.Errorf("Code = %q, want blocked", se.Code)
	}
	if !se.Retryable {
		t.Errorf("Retryable = false, want true (blocked is retryable)")
	}
}

// A genuinely successful result must NOT raise.
func TestScanResultError_Success(t *testing.T) {
	if se := scanResultError(map[string]interface{}{"ok": true, "markdown": "# Hi"}); se != nil {
		t.Errorf("expected nil for a successful result, got %+v", se)
	}
	// No "ok" field and no error code → not an error.
	if se := scanResultError(map[string]interface{}{"markdown": "# Hi"}); se != nil {
		t.Errorf("expected nil when no ok/code present, got %+v", se)
	}
}

// A top-level code that is a PROBLEM-channel code must not be misread as a
// result failure (only result-channel codes flag the result).
func TestScanResultError_IgnoresProblemChannelCode(t *testing.T) {
	if se := scanResultError(map[string]interface{}{"code": "rate_limited"}); se != nil {
		t.Errorf("problem-channel code at top level should not flag a result error, got %+v", se)
	}
}

// A non-2xx problem+json body must carry the canonical code from the body. The
// legacy status-keyed type is preserved (503 → *APIError), but the canonical
// Code/Retryable/RequestID are now exposed on the embedded GhostCrawlError.
func TestRaiseForStatus_ProblemJSON(t *testing.T) {
	body := `{"type":"about:blank","title":"Proxy pool exhausted","status":503,` +
		`"code":"pool_exhausted","retryable":true,"instance":"req_xyz"}`
	err := raiseForStatus(503, body)
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("503 should map to *APIError, got %T", err)
	}
	if ae.Code != CodePoolExhausted {
		t.Errorf("Code = %q, want pool_exhausted", ae.Code)
	}
	if !ae.Retryable {
		t.Errorf("Retryable = false, want true")
	}
	if ae.RequestID != "req_xyz" {
		t.Errorf("RequestID = %q, want req_xyz", ae.RequestID)
	}
}

// A non-2xx in the "other 4xx" range (no legacy type) becomes a *ProblemError
// keyed on the body code.
func TestRaiseForStatus_ProblemError404(t *testing.T) {
	body := `{"code":"not_found","status":404,"instance":"req_1"}`
	err := raiseForStatus(404, body)
	pe, ok := err.(*ProblemError)
	if !ok {
		t.Fatalf("expected *ProblemError, got %T", err)
	}
	if pe.Code != CodeNotFound {
		t.Errorf("Code = %q, want not_found", pe.Code)
	}
	if pe.Retryable {
		t.Errorf("not_found should not be retryable")
	}
}

// When the body is opaque (no JSON), raiseForStatus falls back to the status map
// and still yields a canonical code.
func TestRaiseForStatus_StatusFallback(t *testing.T) {
	err := raiseForStatus(429, "The server returned an unexpected status code: 429")
	rl, ok := err.(*RateLimitError)
	if !ok {
		t.Fatalf("429 should map to *RateLimitError, got %T", err)
	}
	if rl.Code != CodeRateLimited || !rl.Retryable {
		t.Errorf("429 fallback Code=%q retryable=%v, want rate_limited/true", rl.Code, rl.Retryable)
	}
	// 504 with no body → *APIError with engine_timeout (retryable) on the base.
	err = raiseForStatus(504, "")
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("504 should map to *APIError, got %T", err)
	}
	if ae.Code != CodeEngineTimeout || !ae.Retryable {
		t.Errorf("504 fallback Code=%q retryable=%v, want engine_timeout/true", ae.Code, ae.Retryable)
	}
}
