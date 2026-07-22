# ghostcrawl-go

The official Go client for the [GhostCrawl](https://ghostcrawl.io) API.
Collect web data at scale — scrape, crawl, search, and extract structured data.

## Install

```bash
go get github.com/GhostCrawl/ghostcrawl-go/v2@latest
```

Requires Go 1.21+.

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"

    ghostcrawl "github.com/GhostCrawl/ghostcrawl-go/v2"
)

func main() {
    // Token from constructor or GHOSTCRAWL_API_KEY env var
    client, err := ghostcrawl.New("gck_live_YOUR_KEY")
    if err != nil {
        log.Fatal(err)
    }

    // Scrape a URL
    result, err := client.Scrape(context.Background(), ghostcrawl.ScrapeRequest{
        URL:    "https://example.com",
        Format: "markdown",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result["content"])

    // Start a crawl run and wait for it to finish (see "Crawl runs" below)
    run, err := client.CrawlRuns().Start(context.Background(), ghostcrawl.StartCrawlRunRequest{
        URL:      "https://example.com",
        MaxDepth: 2,
        MaxPages: 50,
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("run_id:", run["run_id"])

    // Web search
    results, err := client.Search(context.Background(), ghostcrawl.SearchRequest{
        Query:  "latest AI research",
        Engine: "google",
        Limit:  10,
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(results["results"])
}
```

## Authentication

```go
// Option 1: pass token directly
client, err := ghostcrawl.New("gck_live_YOUR_KEY")

// Option 2: set environment variable (recommended for production)
// export GHOSTCRAWL_API_KEY=gck_live_YOUR_KEY
client, err := ghostcrawl.New("")
```

Every request sends `Authorization: Bearer <token>`. This is the only auth scheme the API accepts.

## Extract structured data

```go
data, err := client.Extract(context.Background(), ghostcrawl.ExtractRequest{
    URL: "https://example.com/product",
    Schema: map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "name":        map[string]interface{}{"type": "string"},
            "price":       map[string]interface{}{"type": "number"},
            "description": map[string]interface{}{"type": "string"},
        },
    },
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(data["name"], data["price"])
```

## Browser utilities — content, screenshot, PDF

```go
// Rendered content as a JSON envelope: {url, status, format, status_code, content, bytes}
page, err := client.Content(context.Background(), ghostcrawl.ContentRequest{
    URL:    "https://example.com",
    Engine: "auto",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(page["status_code"], page["bytes"])

// Screenshot — returns raw PNG bytes
png, err := client.Screenshot(context.Background(), ghostcrawl.ScreenshotRequest{
    URL:      "https://example.com",
    FullPage: true,
})
if err != nil {
    log.Fatal(err)
}
_ = os.WriteFile("page.png", png, 0o644)

// PDF — returns raw application/pdf bytes (Chrome-only; a Firefox/WebKit identity
// is rejected with a 400 pdf_engine_unsupported error)
pdf, err := client.Pdf(context.Background(), ghostcrawl.PdfRequest{
    URL:         "https://example.com",
    PaperFormat: "a4",
})
if err != nil {
    log.Fatal(err)
}
_ = os.WriteFile("page.pdf", pdf, 0o644)
```

## Agent (BYO model, account-gated)

The agent lane runs a natural-language browser task. It is **bring-your-own-model** — pass your
own `provider_config` in the request body — and **account-gated**: the API returns `404 not_found`
unless the capability is enabled for your account. Inspect that status explicitly; it is the
expected "not enabled" answer.

```go
result, err := client.Agent(context.Background(), map[string]interface{}{
    "task": map[string]interface{}{
        "instruction": "click the 'Books to Scrape' link",
        "start_url":   "https://books.toscrape.com",
    },
    // provider_config is BYO — reference your provider key by env-var name, never a literal.
    "provider_config": map[string]interface{}{
        "provider": "openai",
        "api_key":  "OPENAI_API_KEY",
        "model":    "gpt-4o",
    },
})
if err != nil {
    var gErr *ghostcrawl.GhostCrawlError
    if errors.As(err, &gErr) && gErr.StatusCode == 404 {
        fmt.Println("agent lane not enabled for this account (BYO/gated)")
    } else {
        log.Fatal(err)
    }
} else {
    fmt.Println(result)
}
```

## Crawl runs — wait for completion

A crawl runs asynchronously. Rather than hand-write a poll-with-sleep loop, wait
for it **event-driven**: `WaitForCompletion` long-polls
`GET /v1/crawl-runs/{run_id}?wait=true`, which the server holds open until the
run reaches a terminal state (`completed` | `failed` | `cancelled`) or its
window elapses — then re-arms the next window. There is no client-side sleep; the
only blocking is the server-side long-poll, and cancellation is governed by the
`context.Context` (deadline or `cancel()`), which interrupts even an in-flight
window.

```go
run, err := client.CrawlRuns().Start(context.Background(), ghostcrawl.StartCrawlRunRequest{
    URL:      "https://example.com",
    MaxDepth: 2,
    MaxPages: 50,
})
if err != nil {
    log.Fatal(err)
}
runID, _ := run["run_id"].(string)

// The context deadline bounds the total wait.
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()

final, err := client.CrawlRuns().WaitForCompletion(ctx, runID)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        log.Fatal("crawl still running after wait deadline")
    }
    log.Fatal(err)
}
fmt.Println("status:", final["status"]) // completed | failed | cancelled
```

Or block in the **start call itself** — one round-trip, no separate wait:

```go
run, err := client.CrawlRuns().Start(ctx, ghostcrawl.StartCrawlRunRequest{
    URL:               "https://example.com",
    WaitUntilComplete: true,            // sends wait_until: "completed"
    WaitTimeout:       5 * time.Minute, // optional server-side window (timeout_s)
})
```

If the server window elapses before the run finishes, `Start` returns the current
non-terminal run (HTTP 200) — hand `run["run_id"]` to `WaitForCompletion` to keep
waiting. Tune each long-poll window with `WithWaitWindow`:

```go
final, err := client.CrawlRuns().WaitForCompletion(ctx, runID,
    ghostcrawl.WithWaitWindow(60*time.Second))
```

**Alternative:** for fire-and-forget, skip the wait entirely and register a
`client.Webhooks()` endpoint to be notified when the run finishes.

## Error handling

```go
import (
    "errors"
    ghostcrawl "github.com/GhostCrawl/ghostcrawl-go/v2"
)

_, err := client.Scrape(ctx, ghostcrawl.ScrapeRequest{URL: "https://example.com"})
if err != nil {
    var authErr *ghostcrawl.AuthenticationError
    var rateErr *ghostcrawl.RateLimitError
    var payErr *ghostcrawl.PaymentRequiredError
    var apiErr *ghostcrawl.APIError
    switch {
    case errors.As(err, &authErr):
        fmt.Println("Invalid API key — check your token")
    case errors.As(err, &payErr):
        fmt.Println("Usage limit reached — upgrade your plan")
    case errors.As(err, &rateErr):
        fmt.Println("Rate limit reached — retry after a short delay")
    case errors.As(err, &apiErr):
        fmt.Printf("Server error: %d\n", apiErr.StatusCode)
    default:
        fmt.Println("Error:", err)
    }
}
```

## Self-hosted

```go
client, err := ghostcrawl.New("gck_live_YOUR_KEY", "http://localhost:8080")
```

## All resources

| Resource | Method / accessor | Key operations |
|----------|------------------|----------------|
| Scraping | `client.Scrape(ctx, req)` | Render and return page content |
| Web search | `client.Search(ctx, req)` | Search Google, Bing, DuckDuckGo |
| Data extraction | `client.Extract(ctx, req)` | Structured JSON from any page |
| Deep crawl | `client.Crawl(ctx, req)` | Crawl a site depth-first |
| URL map | `client.Map(ctx, req)` | Discover all reachable URLs |
| Content | `client.Content(ctx, req)` | Rendered content JSON envelope |
| Screenshot | `client.Screenshot(ctx, req)` | Capture a URL to PNG bytes |
| PDF | `client.Pdf(ctx, req)` | Render a URL to PDF bytes (Chrome-only) |
| Agent (BYO) | `client.Agent(ctx, task)` | NL browser task — account-gated, BYO model |
| Account | `client.Me(ctx)` | Get account info and usage |
| Crawl runs | `client.CrawlRuns()` | Start, WaitForCompletion, List, Get, Cancel |
| Sessions | `client.Sessions()` | Create, List, Extend, Release |
| Profiles | `client.Profiles()` | List, Get, Create, Update, Delete |
| Webhooks | `client.Webhooks()` | List, Get, Create, Delete, RotateSecret |
| Schedules | `client.Schedules()` | List, Get, Create, Delete |
| Datasets | `client.Datasets()` | List, Get, Create, Delete, Rows, Append |
| Recordings | `client.Recordings()` | List, Get, Delete |
| Key-Value Store | `client.KV()` | Get, Set, Delete |

## License

Proprietary — GhostCrawl Software License. See [LICENSE](LICENSE).
