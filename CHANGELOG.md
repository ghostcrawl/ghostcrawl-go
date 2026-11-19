# GhostCrawl Go SDK Changelog

All notable changes to the GhostCrawl Go client are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/) and this
project follows semantic versioning.

## [Unreleased]

### Added

- **Engine-agnostic in-page actions (parity with the Python SDK)** —
  `page.mouse(ops)` replays a mouse op-stream (move/down/up/wheel/dwell)
  natively on the live page; `page.arrival(dest_url, source_url, ...)` arrives
  at a page through a native link click (document.referrer + Referer +
  Sec-Fetch-* are native); `page.tab_visit` opens a background tab, reads it,
  and closes it (Chrome engine only).

### Notes

- This SDK does NOT provide an AI/LLM backend — model/key passthrough only
  (bring your own). These surfaces are browser primitives, no model.
- Kiota core: the in-page action route builders + body models were added surgically
  (the full-spec regen was not re-run against the whole tree; the customer
  spec input was refreshed for the next regen).

## [2.3.6] - 2026-08-22

### Added

- Full /v1 parity: `SessionPersona(ctx, sessionID)` returns a running session's
  customer-safe persona (`device_class` / `os_class` / `browser_class` / `locale`),
  and `Me(ctx)` surfaces the `max_concurrency` field (your plan's concurrent-session
  ceiling).
- Typed results for `Scrape`, `Search`, and `Crawl` with field names that match every
  other GhostCrawl SDK.
- `Wait` and `WaitTimeout` helpers block a search job until it reaches a terminal state
  via a bounded client-side poll that honors `Retry-After`.

### Changed

- Config precedence is explicit: the `GHOSTCRAWL_API_KEY` environment variable takes
  precedence over the on-disk config file.

### Migrating to 2.3.6

- No request or response field names changed; existing code that reads result fields
  keeps working.
- To block on a search job, call `Wait`/`WaitTimeout` instead of polling the job
  endpoint yourself.
