# go-again/sqlite — common operations.
#
# Install just from https://just.systems. Run `just` (no args) for the
# default recipe (build + test + lint).

# Default recipe: a fast pre-commit gate.
default: build test lint

# List every recipe (just --list shorthand).
help:
    @just --list

# Build every package + every example to catch interface drift.
build:
    go build ./...

# Run the full test suite across every package.
test:
    go test -count=1 -timeout 2m ./...

# Verbose test run for diagnosing a flake.
test-v:
    go test -count=1 -timeout 5m -v ./...

# Run a single named test (or regex). Usage: just test-one TestBLOB_
test-one PATTERN:
    go test -count=1 -timeout 1m -run "{{PATTERN}}" -v ./...

# Race-detector pass on linux/amd64 + darwin/arm64 are the host-runnable ones.
test-race:
    go test -race -count=1 -timeout 5m ./...

# Test with coverage; outputs HTML to /tmp/cover.html.
coverage:
    go test -count=1 -coverprofile=/tmp/cover.out ./...
    go tool cover -html=/tmp/cover.out -o /tmp/cover.html
    @echo "open /tmp/cover.html"

# Run all benchmarks (override duration via `just bench --bench-time=2s`).
bench *FLAGS:
    go test -run=^$ -bench=. -benchmem -count=3 {{FLAGS}} ./...

# Run only the authorizer hot-path benchmark.
bench-auth:
    go test -run=^$ -bench=BenchmarkAuthorizer -benchmem -count=5 .

# Vec hot-path benchmarks (KNN, KNN+filter, BatchInsert JSON/Binary).
bench-vec:
    go test -run=^$ -bench='^Benchmark' -benchmem -count=5 ./vec/

# FTS5 hot-path benchmarks (Search, Search+ranking, Insert batch).
bench-fts:
    go test -run=^$ -bench='^Benchmark' -benchmem -count=5 ./fts/

# Lint with fmt-check + vet + staticcheck + golangci-lint + modernize
# (matches CI). fmt-check runs first because it's the cheapest and the
# most common cause of CI failures from local-only pushes.
lint: fmt-check vet staticcheck golangci modernize

# go vet across all packages. unsafeptr=false suppresses the false-positive
# storm from modernc's uintptr↔unsafe.Pointer conversions inherited in our
# forked wrapper (conn.go, stmt.go, etc.) — those uses are correct and
# unavoidable when talking to the transpiled lib/. golangci-lint already
# applies the same suppression in .golangci.yml.
vet:
    go vet -unsafeptr=false ./...

# staticcheck. Install: `go install honnef.co/go/tools/cmd/staticcheck@latest`
staticcheck:
    @if ! command -v staticcheck >/dev/null; then \
        echo "staticcheck not installed; go install honnef.co/go/tools/cmd/staticcheck@latest"; \
        exit 1; \
    fi
    staticcheck ./...

# golangci-lint. Install: `brew install golangci-lint` or see https://golangci-lint.run.
golangci:
    @if ! command -v golangci-lint >/dev/null; then \
        echo "golangci-lint not installed; see https://golangci-lint.run"; \
        exit 1; \
    fi
    golangci-lint run --timeout 5m

# gopls modernize: catches Go-version-bump idioms (range-over-int,
# reflect.TypeFor, strings.SplitSeq, sync.WaitGroup.Go, etc.). Run via
# `go run` so contributors don't need a separate install step. The grep
# filter skips files we keep verbatim from upstream forks per CLAUDE.md
# — those follow modernc/glebarez patterns and shouldn't drift on style.
#
# `^go:` strips Go's auto-toolchain breadcrumbs (`go: downloading ...`,
# `go: switching to go1.X` etc.) — newer gopls releases require a
# newer Go than we pin in go.mod, so `go run @latest` auto-downloads
# and that chatter would otherwise trip the `[ -n "$out" ]` gate.
modernize:
    @out=$(go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./... 2>&1 \
        | grep -v -E '^/[^:]*/(sqlite|vtab|rows)\.go:' \
        | grep -v -E '^/[^:]*/gorm/(sqlite|migrator)\.go:' \
        | grep -v '^exit status' \
        | grep -v '^go: ' \
        || true); \
    if [ -n "$out" ]; then echo "$out"; exit 1; fi

# gofmt diff (read-only). Fails if any file would be reformatted.
fmt-check:
    @out=$(gofmt -d $(find . -name '*.go' -not -path './.*/*')); \
    if [ -n "$out" ]; then echo "$out"; exit 1; fi

# Apply gofmt in place.
fmt:
    @gofmt -w $(find . -name '*.go' -not -path './.*/*')

# go mod tidy across the module.
tidy:
    go mod tidy

# Run an example by directory name (e.g. `just example vec-search`).
example NAME:
    go run ./examples/{{NAME}}/

# Smoke-test every example (each prints something to stdout when working).
examples:
    @for ex in $(ls -d examples/*/); do \
        echo "=== $ex ==="; \
        go run "./$ex" || (echo "FAILED: $ex"; exit 1); \
    done

# Cross-build every GOOS/GOARCH the CI matrix covers (compile-only).
cross-build:
    @set -e; \
    for triple in \
        darwin/amd64 darwin/arm64 \
        freebsd/amd64 freebsd/arm64 \
        linux/386 linux/amd64 linux/arm linux/arm64 \
        linux/loong64 linux/ppc64le linux/riscv64 linux/s390x \
        windows/386 windows/amd64 windows/arm64; \
    do \
        export GOOS=$(echo "$triple" | cut -d/ -f1); \
        export GOARCH=$(echo "$triple" | cut -d/ -f2); \
        printf "  %-18s " "$triple"; \
        go build ./ ./gorm/... ./fts/... ./vfs/... 2>/dev/null && \
        (go build ./vec/... 2>/dev/null || echo -n "(vec skipped) ") && \
        echo "ok" || echo "FAILED"; \
    done

# Full CI parity: everything CI runs, in order. Slower than `default`.
ci: build test test-race lint cross-build

# Clean test artifacts (.test binaries, cover files).
clean:
    @find . -name '*.test' -not -path './.*' -delete
    @find . -name 'cover.out' -not -path './.*' -delete
    @rm -f /tmp/cover.out /tmp/cover.html

# Bump modernc.org/sqlite to a tag (e.g. `just bump-modernc vX.Y.Z`); libc follows.
bump-modernc VERSION:
    @echo "Bumping modernc.org/sqlite to {{VERSION}}..."
    go get modernc.org/sqlite@{{VERSION}}
    go mod tidy
    @echo
    @echo "Now run: just test cross-build"
    @echo "If anything fails, check the libc pin in modernc.org/sqlite's"
    @echo "go.mod and match it here exactly (see CLAUDE.md libc section)."

# Show what's currently pinned for modernc deps.
modernc-versions:
    @go list -m modernc.org/...

# Quick reference for downstream consumers: print the libc version we pin.
libc-pin:
    @go list -m modernc.org/libc | awk '{print "modernc.org/libc " $2}'
