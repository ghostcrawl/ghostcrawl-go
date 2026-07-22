// Code in this file mirrors ghostcrawl/errors/codes.json (the single source of
// truth — generated from the server-side error catalog). Do NOT hand-diverge the
// set: every code, its HTTP status, its retryability, and its channel are copied
// from that file. When the catalog changes, regenerate this list to match.
//
// Two channels:
//   - "problem": OUR failure — the API answers with a non-2xx body of type
//     application/problem+json carrying {code, retryable, retry_after, ...}.
//   - "result": TARGET failure — the API answers HTTP 200 and the result object
//     carries ok:false plus result_error{code, retryable, target_status?}.
//
// See https://docs.ghostcrawl.io for the customer-facing "Error codes & retries"
// reference.

package ghostcrawl

// Error channel — where a code is surfaced.
const (
	// ChannelProblem marks an error returned as a non-2xx application/problem+json
	// body (the SDK raises a *ProblemError).
	ChannelProblem = "problem"
	// ChannelResult marks an error returned inside an HTTP-200 result object
	// (the SDK raises a *ScrapeError).
	ChannelResult = "result"
)

// Canonical error codes. The string values are the stable wire identifiers used
// by every GhostCrawl surface and SDK.
const (
	// Channel: problem (our failure → non-2xx application/problem+json)
	CodeBadRequest               = "bad_request"
	CodeUnauthorized             = "unauthorized"
	CodeForbidden                = "forbidden"
	CodePaymentRequired          = "payment_required"
	CodeNotFound                 = "not_found"
	CodeConflict                 = "conflict"
	CodeByoProxyInvalid          = "byo_proxy_invalid"
	CodeTierUnavailable          = "tier_unavailable"
	CodeRateLimited              = "rate_limited"
	CodeQuotaBackendUnavailable  = "quota_backend_unavailable"
	CodePoolExhausted            = "pool_exhausted"
	CodeEgressIntegrityFailed    = "egress_integrity_failed"
	CodeRenderHung               = "render_hung"
	CodeEngineCrashed            = "engine_crashed"
	CodeRenderTimeout            = "render_timeout"
	CodeEngineTimeout             = "engine_timeout"
	CodeServiceUnavailable       = "service_unavailable"
	CodeInternalError            = "internal_error"

	// Channel: result (target failure → HTTP 200 + result_error)
	CodeTargetHTTPError = "target_http_error"
	CodeNavigationFailed = "navigation_failed"
	CodeBlocked          = "blocked"
	CodeCaptchaRequired  = "captcha_required"
	CodeEmptyContent     = "empty_content"
)

// errorCodeSpec is one row of the canonical catalog.
type errorCodeSpec struct {
	Code      string
	HTTP      int
	Retryable bool
	Channel   string
}

// errorCatalog mirrors codes.json. Keyed by code for O(1) lookup.
var errorCatalog = map[string]errorCodeSpec{
	CodeBadRequest:              {CodeBadRequest, 400, false, ChannelProblem},
	CodeUnauthorized:            {CodeUnauthorized, 401, false, ChannelProblem},
	CodeForbidden:               {CodeForbidden, 403, false, ChannelProblem},
	CodePaymentRequired:         {CodePaymentRequired, 402, false, ChannelProblem},
	CodeNotFound:                {CodeNotFound, 404, false, ChannelProblem},
	CodeConflict:                {CodeConflict, 409, false, ChannelProblem},
	CodeByoProxyInvalid:         {CodeByoProxyInvalid, 422, false, ChannelProblem},
	CodeTierUnavailable:         {CodeTierUnavailable, 400, false, ChannelProblem},
	CodeRateLimited:             {CodeRateLimited, 429, true, ChannelProblem},
	CodeQuotaBackendUnavailable: {CodeQuotaBackendUnavailable, 503, true, ChannelProblem},
	CodePoolExhausted:           {CodePoolExhausted, 503, true, ChannelProblem},
	CodeEgressIntegrityFailed:   {CodeEgressIntegrityFailed, 503, true, ChannelProblem},
	CodeRenderHung:              {CodeRenderHung, 503, true, ChannelProblem},
	CodeEngineCrashed:           {CodeEngineCrashed, 503, true, ChannelProblem},
	CodeRenderTimeout:           {CodeRenderTimeout, 504, true, ChannelProblem},
	CodeEngineTimeout:            {CodeEngineTimeout, 504, true, ChannelProblem},
	CodeServiceUnavailable:      {CodeServiceUnavailable, 503, true, ChannelProblem},
	CodeInternalError:           {CodeInternalError, 500, true, ChannelProblem},
	CodeTargetHTTPError:         {CodeTargetHTTPError, 200, false, ChannelResult},
	CodeNavigationFailed:        {CodeNavigationFailed, 200, false, ChannelResult},
	CodeBlocked:                 {CodeBlocked, 200, true, ChannelResult},
	CodeCaptchaRequired:         {CodeCaptchaRequired, 200, true, ChannelResult},
	CodeEmptyContent:            {CodeEmptyContent, 200, false, ChannelResult},
}

// codeForStatus maps an HTTP status to the canonical problem-channel code that
// best describes it. Used as a fallback when a non-2xx response body is not
// available (some transports discard the problem+json body), so callers still
// get a canonical code + retryable flag keyed off the status line.
func codeForStatus(status int) string {
	switch status {
	case 400:
		return CodeBadRequest
	case 401:
		return CodeUnauthorized
	case 402:
		return CodePaymentRequired
	case 403:
		return CodeForbidden
	case 404:
		return CodeNotFound
	case 409:
		return CodeConflict
	case 422:
		return CodeByoProxyInvalid
	case 429:
		return CodeRateLimited
	case 503:
		return CodeServiceUnavailable
	case 504:
		return CodeEngineTimeout
	default:
		if status >= 500 {
			return CodeInternalError
		}
		return CodeBadRequest
	}
}

// isRetryableCode reports the catalog's retryable flag for a code (false for
// unknown codes).
func isRetryableCode(code string) bool {
	if spec, ok := errorCatalog[code]; ok {
		return spec.Retryable
	}
	return false
}
