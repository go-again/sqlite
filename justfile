# gosqlite — common operations.
#
# Install just from https://just.systems. Run `just` (no args) for the
# default recipe (build + test + lint).

set dotenv-load := true

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

# Run an example by leaf name or sub-path. examples/ is grouped
# (migrating/, getting-started/, features/<area>/), so both
# `just example hash` and `just example features/extensions/hash` work;
# an ambiguous leaf (e.g. `crypto`) lists the candidates.
example NAME:
    @m=$(find examples -type d | sed 's|^examples/||' | grep -E '(^|/){{NAME}}$' || true); \
    c=$(printf '%s\n' "$m" | grep -c . || true); \
    if [ "$c" -eq 0 ]; then echo "no example matching '{{NAME}}' — see: just examples-list"; exit 1; \
    elif [ "$c" -gt 1 ]; then echo "ambiguous '{{NAME}}', pick one:"; printf '  %s\n' $m; exit 1; \
    elif [ -f "examples/$m/go.mod" ]; then ( cd "examples/$m" && go run . ); \
    else go run "./examples/$m/"; fi

# List every runnable example (its sub-path under examples/).
examples-list:
    @find examples -name main.go | while read -r f; do [ -f "${f%/main.go}/go.mod" ] || echo "$f"; done | sed 's|/main.go||; s|^examples/||' | sort

# Examples that write .db files (crypto/cksm/backup) would otherwise leave
# debris in the repo PWD — we redirect each one into a per-example temp
# dir and clean up afterwards.
# Smoke-test every example (each prints something to stdout when working).
# Discovery is depth-agnostic (find main.go) so the grouped layout works; an
# example with its own go.mod (e.g. examples/liteorm, a separate module) is
# skipped here and built on its own (`just example liteorm`).
examples:
    @repo="$(pwd)"; \
    for ex in $(find examples -name main.go | while read -r f; do [ -f "${f%/main.go}/go.mod" ] || echo "${f%/main.go}"; done | sort); do \
        echo "=== $ex ==="; \
        sandbox="$(mktemp -d)"; \
        ( cd "$repo" && go build -o "$sandbox/example" "./$ex" ) && ( cd "$sandbox" && ./example ) \
            || (echo "FAILED: $ex"; rm -rf "$sandbox"; exit 1); \
        rm -rf "$sandbox"; \
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

# Run the isolated xorm-compatibility module (its own go.mod with a
# replace, so xorm.io/xorm never enters the main module's graph). Needs
# network for xorm.io/xorm on first run.
xorm-compat:
    cd xorm-compat && go test -count=1 -timeout 5m ./...

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

# Prepare a release: pin every intra-repo gosqlite.org require to VERSION across
# the publishable modules (the root and gorm/), verify they still build, then
# PRINT the exact ordered tag/push plan. It edits go.mod only — it never commits,
# tags, or pushes (run the printed git commands yourself). The dev-only `replace
# gosqlite.org => ..` directives are ignored by consumers, so they stay. Modules
# are discovered from each go.mod, so this adapts as modules come and go; the
# xorm-compat and examples/* modules are not `gosqlite.org/*`, so they are never
# tagged or imported and are left untouched.
#
#   just release v0.8.0
release VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    v='{{VERSION}}'
    semver='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
    [[ "$v" =~ $semver ]] || { echo "✗ VERSION must look like v1.2.3 or v1.2.3-rc.1 (got '$v')" >&2; exit 1; }

    # Publishable = module path is gosqlite.org or gosqlite.org/*. (xorm-compat is
    # module 'xormcompat', examples/liteorm is 'liteormexample' — neither matches,
    # so both are skipped automatically. Hidden dirs like .liteorm are excluded.)
    pub=()
    while IFS= read -r gomod; do
        mp=$(awk '/^module /{print $2; exit}' "$gomod")
        case "$mp" in gosqlite.org|gosqlite.org/*) pub+=("$(dirname "$gomod")") ;; esac
    done < <(find . -name go.mod -not -path './.*/*' | sort)

    echo "→ publishable modules: ${pub[*]}"
    echo "→ pinning intra-repo gosqlite.org requires to $v"
    bumped=0
    for d in "${pub[@]}"; do
        # Each REQUIRED gosqlite.org* path (require lines read "<path> v<ver>"; the
        # 'module' line and '=>' replace lines carry no " v<ver>", so neither matches).
        for p in $(grep -oE "gosqlite\.org(/[A-Za-z0-9._/-]+)? v[0-9][^[:space:]]*" "$d/go.mod" | sed -E 's/ v.*//' | sort -u); do
            (cd "$d" && go mod edit -require="$p@$v"); echo "    $d: $p → $v"; bumped=1
        done
    done
    [ "$bumped" -eq 1 ] || echo "    (no intra-repo gosqlite.org requires to bump)"

    echo "→ verifying every publishable module still builds (replace directives resolve locally)"
    for d in "${pub[@]}"; do ( cd "$d" && go build ./... ); done

    echo
    echo "→ go.mod changes:"
    git diff --stat -- '*go.mod' 2>/dev/null || echo "    (not a git repo — review go.mod diffs by hand)"

    echo
    echo "════════ RELEASE PLAN — run these yourself; nothing below was executed ════════"
    echo "  git add -A && git commit -m 'release $v'"
    for d in "${pub[@]}"; do
        case "$d" in .) echo "  git tag $v                       # root module: gosqlite.org" ;; \
                      *) echo "  git tag ${d#./}/$v                  # ${d#./} module: $(awk '/^module /{print $2; exit}' "$d/go.mod")" ;; esac
    done
    echo "  git push origin HEAD --tags        # commit + every tag pushed together"
    echo
    echo "  Consumers can 'go get' these once the gosqlite.org go-import vanity meta"
    echo "  resolves the module path to the GitHub repo and $v is a published tag."
