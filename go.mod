module github.com/GhostCrawl/ghostcrawl-go/v2

// sdk-version: 2.3.2 (Go modules version by git tag, not an in-source constant;
// this comment line is the mechanical version-stamp gate source of truth for
// check_version_stamp.sh — keep it in sync with the pushed v2.3.2 tag).
go 1.21

retract [v2.2.0, v2.3.2] // internal-architecture descriptions leaked in generated doc-comments; upgrade to v2.3.3+

// v2.2.0 was published with an incomplete module tree; use v2.2.1+.

require (
	github.com/google/uuid v1.6.0
	github.com/microsoft/kiota-abstractions-go v1.7.0
	github.com/microsoft/kiota-http-go v1.4.3
	github.com/microsoft/kiota-serialization-form-go v1.0.0
	github.com/microsoft/kiota-serialization-json-go v1.0.7
	github.com/microsoft/kiota-serialization-multipart-go v1.0.0
	github.com/microsoft/kiota-serialization-text-go v1.0.0
)

require (
	github.com/cjlapao/common-go v0.0.39 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-logr/logr v1.4.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/std-uritemplate/std-uritemplate/go v0.0.57 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	go.opentelemetry.io/otel v1.24.0 // indirect
	go.opentelemetry.io/otel/metric v1.24.0 // indirect
	go.opentelemetry.io/otel/trace v1.24.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
