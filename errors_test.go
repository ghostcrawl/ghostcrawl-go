package ghostcrawl

import "testing"

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
