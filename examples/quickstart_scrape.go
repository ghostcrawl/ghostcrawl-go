//go:build ignore
// +build ignore

// quickstart_scrape.go — Scrape a URL and print the content.
//
// Usage:
//
//	export GHOSTCRAWL_API_KEY=gck_live_YOUR_KEY
//	go run quickstart_scrape.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	ghostcrawl "github.com/GhostCrawl/ghostcrawl-go/v2"
)

func main() {
	apiKey := os.Getenv("GHOSTCRAWL_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: GHOSTCRAWL_API_KEY environment variable is not set.")
		fmt.Fprintln(os.Stderr, "Get your key at https://ghostcrawl.io and set it with:")
		fmt.Fprintln(os.Stderr, "  export GHOSTCRAWL_API_KEY=gck_live_YOUR_KEY")
		os.Exit(1)
	}

	client, err := ghostcrawl.New(apiKey)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	result, err := client.Scrape(ctx, ghostcrawl.ScrapeRequest{
		URL:    "https://example.com",
		Format: "markdown",
	})
	if err != nil {
		log.Fatalf("scrape failed: %v", err)
	}

	// Canonical LLM-ready markdown scrape. The markdown envelope carries identity_id so
	// this drive can be correlated to its egress-exit assignment (D-04).
	content, _ := result["markdown"].(string)
	if content == "" {
		content, _ = result["content"].(string)
	}
	if len(content) > 200 {
		content = content[:200] + "..."
	}
	fmt.Println("Status:", result["status"])
	// identity_id (Phase 140.4-16 response envelope field) — printed so a caller can
	// correlate this exact drive to its server-side egress-exit assignment (D-04, phase 177).
	// JSON numbers decode as float64; render a whole-number id as a plain integer.
	identityID := result["identity_id"]
	if f, ok := identityID.(float64); ok && f == float64(int64(f)) {
		identityID = int64(f)
	}
	fmt.Println("identity_id:", identityID)
	fmt.Println("Content preview:")
	fmt.Println(content)
}
