package ghostcrawl

// Typed-but-map-accessible result models for the GhostCrawl Go SDK.
//
// Field names are shared across the GhostCrawl SDKs so results read uniformly.
//
// Go cannot make a struct index-accessible the way Python's Mapping or a JS object
// with an index signature can, so back-compat is provided by an attached
// Raw map[string]interface{} (the complete decoded response). New code reads the
// discoverable typed fields (result.Content, result.StatusCode, …); existing code
// that treated the response as a dynamic map reads result.Raw["whatever"] — no
// server field is ever lost. The raw kiota _generated/ models are NEVER returned to
// the user; the facade wraps every response into one of these owned types.

// rawMapper is implemented by every typed result so generic code (and the parity
// e2e harness) can recover the full decoded response map.
type rawMapper interface {
	ToMap() map[string]interface{}
}

// ScrapeResult is the result of Scrape / Content — category "scrape".
//
// Canonical fields: status, content, markdown, html, status_code, bytes, url,
// format, warnings. content is the cross-format alias for the rendered output
// (markdown/html/text), surfaced on construction.
type ScrapeResult struct {
	Status     string        `json:"status,omitempty"`
	Content    string        `json:"content,omitempty"`
	Markdown   string        `json:"markdown,omitempty"`
	HTML       string        `json:"html,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	Bytes      int           `json:"bytes,omitempty"`
	URL        string        `json:"url,omitempty"`
	Format     string        `json:"format,omitempty"`
	Warnings   []interface{} `json:"warnings,omitempty"`
	// Raw is the complete decoded response — the back-compat / undeclared-field door.
	Raw map[string]interface{} `json:"-"`
}

// ToMap returns the complete decoded response map (back-compat access).
func (r *ScrapeResult) ToMap() map[string]interface{} { return r.Raw }

// Lane returns the category this result belongs to. Useful for uniform
// logging and dispatch across the SDKs, which share these category names.
func (r *ScrapeResult) Lane() string { return "scrape" }

// SearchResult is the result of Search — category "search" (status, results, query).
type SearchResult struct {
	Status  string        `json:"status,omitempty"`
	Results []interface{} `json:"results,omitempty"`
	Query   string        `json:"query,omitempty"`
	// Raw is the complete decoded response — the back-compat / undeclared-field door.
	Raw map[string]interface{} `json:"-"`
}

// ToMap returns the complete decoded response map (back-compat access).
func (r *SearchResult) ToMap() map[string]interface{} { return r.Raw }

// Lane returns the category this result belongs to ("search").
func (r *SearchResult) Lane() string { return "search" }

// CrawlRun is a crawl run — category "crawl" (run_id, status, results). Returned
// by Crawl.
type CrawlRun struct {
	RunID   string        `json:"run_id,omitempty"`
	Status  string        `json:"status,omitempty"`
	Results []interface{} `json:"results,omitempty"`
	// Raw is the complete decoded response — the back-compat / undeclared-field door.
	Raw map[string]interface{} `json:"-"`
}

// ToMap returns the complete decoded response map (back-compat access).
func (r *CrawlRun) ToMap() map[string]interface{} { return r.Raw }

// Lane returns the category this result belongs to ("crawl").
func (r *CrawlRun) Lane() string { return "crawl" }

// Document is a single crawled/searched document — category "document"
// (title, url, content).
type Document struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	Content string `json:"content,omitempty"`
	// Raw is the complete decoded response — the back-compat / undeclared-field door.
	Raw map[string]interface{} `json:"-"`
}

// ToMap returns the complete decoded response map (back-compat access).
func (r *Document) ToMap() map[string]interface{} { return r.Raw }

// BinaryResult documents the canonical parity field names for the binary lanes
// (pdf / screenshot): bytes and content_type. The binary methods (Pdf, Screenshot)
// return a raw []byte for back-compat; this type names the shared parity fields so
// the binary surface reads uniformly with the other typed results across the GhostCrawl SDKs.
type BinaryResult struct {
	Bytes       []byte `json:"bytes,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// Raw is the complete decoded response — the back-compat / undeclared-field door.
	Raw map[string]interface{} `json:"-"`
}

// ToMap returns the complete decoded response map (back-compat access).
func (r *BinaryResult) ToMap() map[string]interface{} { return r.Raw }

// ---------------------------------------------------------------------------
// Wrap helpers — the facade calls these on the raw decoded map so the raw
// generated model is never returned to the user. A nil map yields an empty
// (non-nil) typed result so field access never panics.
// ---------------------------------------------------------------------------

func newScrapeResult(m map[string]interface{}) *ScrapeResult {
	if m == nil {
		m = map[string]interface{}{}
	}
	r := &ScrapeResult{Raw: m}
	r.Status, _ = m["status"].(string)
	r.Content, _ = m["content"].(string)
	r.Markdown, _ = m["markdown"].(string)
	r.HTML, _ = m["html"].(string)
	r.StatusCode = asInt(m["status_code"])
	r.Bytes = asInt(m["bytes"])
	r.URL, _ = m["url"].(string)
	r.Format, _ = m["format"].(string)
	if w, ok := m["warnings"].([]interface{}); ok {
		r.Warnings = w
	}
	return r
}

func newSearchResult(m map[string]interface{}) *SearchResult {
	if m == nil {
		m = map[string]interface{}{}
	}
	r := &SearchResult{Raw: m}
	r.Status, _ = m["status"].(string)
	r.Query, _ = m["query"].(string)
	// /v1/search carries hits under "items"; older/other lanes use "results".
	// Fall back to "items" and mirror it into "results" (and Raw) so both the
	// typed field and Raw["results"] read consistently — matching the Python
	// SDK's items→results alias.
	res, ok := m["results"].([]interface{})
	if !ok {
		if items, ok2 := m["items"].([]interface{}); ok2 {
			res, ok = items, true
			m["results"] = items
		}
	}
	if ok {
		r.Results = res
	}
	// Stable shape: a cold-rotation running handle ({status:"running", job_id,
	// poll_url,...}) carries no "items"/"results". Guarantee a non-nil (empty)
	// slice so callers never range over a nil Results / read a nil Raw["results"].
	if r.Results == nil {
		r.Results = []interface{}{}
		m["results"] = r.Results
	}
	return r
}

func newCrawlRun(m map[string]interface{}) *CrawlRun {
	if m == nil {
		m = map[string]interface{}{}
	}
	r := &CrawlRun{Raw: m}
	r.RunID, _ = m["run_id"].(string)
	r.Status, _ = m["status"].(string)
	if res, ok := m["results"].([]interface{}); ok {
		r.Results = res
	}
	return r
}
