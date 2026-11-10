// This directory is an npm project, not a Go package. The nested module
// marker keeps `go build ./...` / `go test ./...` at the repository root from
// walking into node_modules, where promptfoo ships Go sources for its
// providers. Do not add Go code here.
module github.com/eli-l/elilap.dev-agent/tests-ai

go 1.27
