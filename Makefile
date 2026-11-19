# go lane one-command parity runner.
#
# `make parity-e2e` runs the TestParityE2E sweep in parity_e2e_test.go, which
# exercises EVERY user-surface row in scripts/phase178/rowset.json ONCE against the
# REAL cloud test tenant and writes one transcript per row to 178-EVIDENCE/go/
# (per scripts/phase178/TRANSCRIPT-SCHEMA.md).
#
# Secrets: the token is read from GHOSTCRAWL_API_KEY (NEVER hardcoded); if
# it is unset the test SKIPS and fabricates nothing. The real-backend sweep is
# wrapped in the shared flock run-lock (scripts/phase178/parity-run.sh) so
# concurrent-wave lanes serialize their hits to the single shared tenant (W2).
# NOTE: this is a live-network integration run and is EXPECTED to exceed the 30s
# soft-latency guidance — network-bound, not a hang (W5).
#
#   export GHOSTCRAWL_API_KEY='<locked-down test-tenant token>'   # revoked after run
#   export GHOSTCRAWL_BASE_URL='<cloud test tenant base URL>'     # optional
#   make -C sdks/go parity-e2e            # all rows
#   make -C sdks/go parity-e2e ROW=pdf    # one row
SHELL := /bin/bash
REPO_ROOT := $(shell cd ../.. && pwd)

.PHONY: parity-e2e build

build:
	go build ./...

# -count=1 disables the test cache so every invocation is a real live-network sweep.
# -timeout 30m matches the live-network budget (W5). ROW is threaded to the runner env.
parity-e2e:
	@ROW="$(ROW)" $(REPO_ROOT)/scripts/phase178/parity-run.sh go -- go test -run TestParityE2E -count=1 -timeout 30m -v ./
