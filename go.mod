module github.com/GhostCrawl/ghostcrawl-go/v2

// sdk-version: 2.3.8 (Go modules version by git tag, not an in-source constant;
// this comment line is the mechanical version-stamp gate source of truth for
// check_version_stamp.sh — keep it in sync with the v2.3.6 tag on publish).
go 1.25.0

retract [v2.2.0, v2.3.3] // superseded — upgrade to the latest release

// v2.2.0 was published with an incomplete module tree; use the latest release.

require (
	github.com/google/uuid v1.6.0
	github.com/microsoft/kiota-abstractions-go v1.9.4
	github.com/microsoft/kiota-http-go v1.5.6
	github.com/microsoft/kiota-serialization-form-go v1.0.0
	github.com/microsoft/kiota-serialization-json-go v1.0.7
	github.com/microsoft/kiota-serialization-multipart-go v1.0.0
	github.com/microsoft/kiota-serialization-text-go v1.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/std-uritemplate/std-uritemplate/go/v2 v2.0.3 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
