//go:build ignore
// +build ignore

// quickstart_crawl.go — Start a crawl run and wait for it to finish.
//
// Waiting is event-driven server-side: WaitForCompletion long-polls
// GET /v1/crawl-runs/{run_id}?wait=true, which the server holds open until the
// run reaches a terminal state. No client-side sleep-poll loop. The overall
// bound is set with a context deadline. (For fire-and-forget instead, register a
// webhook and skip the wait — see client.Webhooks().)
//
// Usage:
//
//	export GHOSTCRAWL_API_KEY=gck_live_YOUR_KEY
//	go run quickstart_crawl.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

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

	// Start a crawl run.
	run, err := client.CrawlRuns().Start(context.Background(), ghostcrawl.StartCrawlRunRequest{
		URL:      "https://example.com",
		MaxDepth: 2,
		MaxPages: 50,
	})
	if err != nil {
		log.Fatalf("crawl start failed: %v", err)
	}

	runID, _ := run["run_id"].(string)
	status, _ := run["status"].(string)
	fmt.Printf("Crawl run started\n  run_id: %s\n  status: %s\n", runID, status)

	// Wait for the run to finish. The context deadline bounds the total wait;
	// cancel it (or Ctrl-C) to stop waiting early.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	final, err := client.CrawlRuns().WaitForCompletion(ctx, runID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Fatalf("crawl still running after wait deadline (run_id=%s)", runID)
		}
		log.Fatalf("waiting for crawl failed: %v", err)
	}

	finalStatus, _ := final["status"].(string)
	fmt.Printf("Crawl finished\n  status: %s\n", finalStatus)
	if finalStatus != "completed" {
		os.Exit(1) // failed or cancelled
	}
	if pages, ok := final["pages_crawled"]; ok {
		fmt.Printf("  pages_crawled: %v\n", pages)
	}

	// One-shot alternative — Start can block server-side itself:
	//
	//	run, err := client.CrawlRuns().Start(ctx, ghostcrawl.StartCrawlRunRequest{
	//	    URL:               "https://example.com",
	//	    WaitUntilComplete: true,             // sends wait_until: "completed"
	//	    WaitTimeout:       5 * time.Minute,  // server-side blocking window
	//	})
}
