# gosqlite — common operations.
#
# Install just from https://just.systems. Run `just` (no args) for the
# default recipe (build + test + lint).

set dotenv-load := true

# The repo's sub-modules — each has its own go.mod (a separate dependency graph).
# Most join the root via a `replace gosqlite.org` directive; a few (e.g.
# crypto/keyring) are independent but still published under gosqlite.org/.
# DISCOVERED, never hand-listed, so adding or removing a module needs no edit to
# the recipes below.
#   submods  — every sub-module (joined via replace, OR published as
#              gosqlite.org/<sub>): built + tested + linted in its own context.
#   pubmods  — only the PUBLISHED ones (module path `gosqlite.org/<sub>`): these
#              also get cross-built and lockstep-pinned. The xorm-compat harness
#              (module `xormcompat`) is tested but never shipped, so it's not here.
#   exmods   — the examples/* modules (own go.mod, external deps like gorm /
#              liteorm). Out of submods/pubmods on purpose (not shipped, not
#              pinned), but still lint + compile-checked via `lint-examples` so
#              the consumer-facing examples can't silently rot.
# submods/pubmods exclude the root, the hidden reference clones (.xorm, …), and
# the examples/* modules (those are exmods, run via `just example`).
submods := `for f in $(find . -mindepth 2 -name go.mod -not -path './.*/*' -not -path './examples/*'); do { grep -q 'replace gosqlite.org ' "$f" || grep -q '^module gosqlite[.]org/' "$f"; } && dirname "$f"; done | sed 's|^[.]/||' | sort | tr '\n' ' '`
pubmods := `for f in $(find . -mindepth 2 -name go.mod -not -path './.*/*' -not -path './examples/*'); do grep -q '^module gosqlite[.]org/' "$f" && dirname "$f"; done | sed 's|^[.]/||' | sort | tr '\n' ' '`
exmods := `find examples -name go.mod -not -path '*/.*/*' | sed 's|/go.mod$||; s|^[.]/||' | sort | tr '\n' ' '`

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

# vfs/vault option matrix: compress? × encrypt? × auth?, plus raw + vfs/crypto baselines, read/write.
bench-vault:
    go test -run=^$ -bench=BenchmarkMatrix -benchmem -count=3 ./vfs/vault/

# Lint the root module + every sub-module with fmt-check + vet + staticcheck +
# golangci-lint + modernize (matches CI). fmt-check runs first because it's the
# cheapest and the most common cause of CI failures from local-only pushes;
# lint-submodules runs last because it lints N modules and is the slowest.
lint: fmt-check vet staticcheck golangci modernize lint-submodules lint-examples

# go vet across all packages. unsafeptr=false suppresses the false-positive
# storm from modernc's uintptr↔unsafe.Pointer conversions inherited in our
# forked wrapper (conn.go, stmt.go, etc.) — those uses are correct and
# unavoidable when talking to the transpiled lib/. golangci-lint already
# applies the same suppression in .golangci.yml.
vet:
    go vet -unsafeptr=false ./...

# staticcheck. Prefers an installed binary (PATH or GOPATH/bin — the latter is
# often not on a fresh login shell's PATH), falling back to `go run` so the
# recipe never depends on what happens to be on PATH. Install for speed:
# `go install honnef.co/go/tools/cmd/staticcheck@latest`.
staticcheck:
    @bin=$(command -v staticcheck || echo "$(go env GOPATH)/bin/staticcheck"); \
    if [ -x "$bin" ]; then "$bin" ./...; \
    else go run honnef.co/go/tools/cmd/staticcheck@latest ./...; fi

# golangci-lint (v2). Same PATH-independent shape as staticcheck. The `go run`
# fallback pins the v2 module path. Install for speed: `brew install
# golangci-lint` or see https://golangci-lint.run.
golangci:
    @bin=$(command -v golangci-lint || echo "$(go env GOPATH)/bin/golangci-lint"); \
    if [ -x "$bin" ]; then "$bin" run --timeout 5m; \
    else go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run --timeout 5m; fi

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
        ok=1; \
        go build ./ ./fts/... ./vfs/... 2>/dev/null || ok=0; \
        for d in {{pubmods}}; do (cd "$d" && go build ./ 2>/dev/null) || ok=0; done; \
        (go build ./vec/... 2>/dev/null || echo -n "(vec skipped) "); \
        [ "$ok" -eq 1 ] && echo "ok" || echo "FAILED"; \
    done

# Print the discovered sub-modules, one per line (debugging the discovery above).
submodules:
    @for d in {{submods}}; do echo "$d"; done

# Test ONE sub-module by its dir, e.g. `just submodule vfs/crypto`. Each runs in
# its own module context, so its separate deps (az / adiantum / xorm) stay out of
# the root graph. (Replaces the old per-module `blobstore` / `crypto` recipes.)
submodule DIR:
    cd {{DIR}} && go test -count=1 -timeout 5m ./...

# Test EVERY sub-module. xorm-compat needs network for xorm.io/xorm on first run;
# vfs/crypto self-skips its blob-I/O tests under -race via TestMain.
test-submodules:
    @set -e; for d in {{submods}}; do echo "=== test $d ==="; (cd "$d" && go test -count=1 -timeout 5m ./...); done

# Lint EVERY sub-module in its own module context: vet + staticcheck +
# golangci-lint + modernize (gofmt is already repo-wide via fmt-check). Mirrors
# the per-module lint the CI `submodules` matrix runs. Slow — lints N modules.
# staticcheck/golangci-lint resolve the same PATH-independent way as the root
# recipes (installed binary if present, else `go run`), so this never depends on
# what's on PATH.
lint-submodules:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(pwd)"
    sc=$(command -v staticcheck || echo "$(go env GOPATH)/bin/staticcheck")
    gc=$(command -v golangci-lint || echo "$(go env GOPATH)/bin/golangci-lint")
    run_staticcheck() { if [ -x "$sc" ]; then "$sc" "$@"; else go run honnef.co/go/tools/cmd/staticcheck@latest "$@"; fi; }
    run_golangci() { if [ -x "$gc" ]; then "$gc" "$@"; else go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest "$@"; fi; }
    for d in {{submods}}; do
        echo "=== lint $d ==="
        (
            cd "$d"
            go vet -unsafeptr=false ./...
            run_staticcheck ./...
            # Pin the config so resolution can't drift if a sub-module ever
            # gains its own .golangci.yml — always use the repo-root one.
            run_golangci run --timeout 5m --config "$root/.golangci.yml" ./...
            # Modernize, minus the forked upstream files we keep verbatim (gorm's
            # sqlite.go/migrator.go; matched on the path tail since these run with
            # the sub-module as the working dir).
            out=$(go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./... 2>&1 \
                | grep -v -E '(^|/)(sqlite|vtab|rows|migrator)\.go:' \
                | grep -v '^exit status' | grep -v '^go: ' || true)
            [ -z "$out" ] || { echo "$out"; exit 1; }
        )
    done

# Lint + compile-check every examples/* module in its own context. These are
# separate modules (own go.mod, external deps like gorm / liteorm), so the root
# `./...` can't reach them and they're outside `submods` — without this they'd
# get no vet/lint/build anywhere. The build guards that the runnable examples
# still compile, writing binaries to a throwaway dir (a bare `go build ./...`
# would drop an executable in each example dir); the rest is the same gate as
# `lint-submodules`. Same PATH-independent tool resolution.
lint-examples:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$(pwd)"
    sc=$(command -v staticcheck || echo "$(go env GOPATH)/bin/staticcheck")
    gc=$(command -v golangci-lint || echo "$(go env GOPATH)/bin/golangci-lint")
    run_staticcheck() { if [ -x "$sc" ]; then "$sc" "$@"; else go run honnef.co/go/tools/cmd/staticcheck@latest "$@"; fi; }
    run_golangci() { if [ -x "$gc" ]; then "$gc" "$@"; else go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest "$@"; fi; }
    for d in {{exmods}}; do
        echo "=== lint $d ==="
        (
            cd "$d"
            tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
            go build -o "$tmp/" ./...
            go vet -unsafeptr=false ./...
            run_staticcheck ./...
            run_golangci run --timeout 5m --config "$root/.golangci.yml" ./...
            out=$(go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./... 2>&1 \
                | grep -v -E '(^|/)(sqlite|vtab|rows|migrator)\.go:' \
                | grep -v '^exit status' | grep -v '^go: ' || true)
            [ -z "$out" ] || { echo "$out"; exit 1; }
        )
    done

# Full CI parity: everything CI runs, in order. Slower than `default`. Now mirrors
# CI's submodule + pin coverage (the old `ci` skipped xorm-compat and the pins).
ci: build test test-race lint cross-build test-submodules check-pins

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

# Verify every PUBLISHED sub-module pins the SAME intra-repo gosqlite.org core
# version. This is the standing guard against the failure that once made
# `go get gosqlite.org/gorm` break: gorm's go.mod required an older core that
# still bundled gorm/, so the import path was claimed by two modules at once
# ("ambiguous import"). `just release` pins them in lockstep by construction;
# this catches any drift introduced by hand between releases. Run in `just ci`.
#
# Only modules whose path is `gosqlite.org/<sub>` are checked — those are the
# ones consumers resolve through tags, so their pin must match. The xorm-compat
# and examples/* modules always resolve via `replace`, so their core require is
# cosmetic (xorm-compat deliberately pins v0.0.0) and is intentionally ignored.
check-pins:
    #!/usr/bin/env bash
    set -euo pipefail
    # Portable by design — deliberately NO bash-4 associative arrays. macOS ships
    # bash 3.2, where `declare -A` silently degrades to an indexed array, which
    # would make this guard pass even on real drift. Collect the versions into a
    # newline string and count the DISTINCT ones with `sort -u`.
    vers=""
    while IFS= read -r f; do
        grep -q '^module gosqlite[.]org/' "$f" || continue
        # The CORE require reads "gosqlite.org vX.Y.Z" — a space after .org, so a
        # "gosqlite.org/<sub> vX" require (none today) would not match here.
        v=$(grep -oE 'gosqlite\.org v[0-9][^[:space:]]*' "$f" | awk '{print $2}' | head -n1 || true)
        [ -n "$v" ] || continue
        printf '  %-14s gosqlite.org %s\n' "$(dirname "$f")" "$v"
        vers="${vers}${v}"$'\n'
    done < <(find . -mindepth 2 -name go.mod -not -path './.*/*' -not -path './examples/*' | sort)
    distinct=$(printf '%s' "$vers" | sed '/^$/d' | sort -u | wc -l | tr -d ' ')
    if [ "$distinct" -gt 1 ]; then
        echo "✗ published sub-modules pin DIFFERENT core versions — re-pin in lockstep with 'just release vX.Y.Z'." >&2
        exit 1
    fi
    echo "✓ all published sub-modules pin the same gosqlite.org core version"

# Cut a release: pin every intra-repo gosqlite.org require to VERSION across the
# publishable modules, verify each still builds, commit the pin, then tag EVERY
# module at that commit and push (commit + all tags) in one shot. Modules are
# discovered from their go.mod, so this adapts as modules come and go; the
# xorm-compat and examples/* modules are not `gosqlite.org/*`, so they are never
# pinned, tagged, or pushed. The dev-only `replace gosqlite.org => ..` directives
# are ignored by consumers, so they stay.
#
# Two release rules this enforces, both learned the hard way:
#   1. Tag EVERY module, together. A module in a subdirectory (gorm/) is only
#      resolvable through a path-prefixed tag (gorm/vX.Y.Z); tagging the root but
#      forgetting the submodule makes `go get gosqlite.org/gorm` fail with "no
#      matching versions" — the module looks like it vanished. The single tag
#      loop here makes forgetting one impossible.
#   2. Pin lockstep. A submodule must require a core version new enough to have
#      carved that submodule OUT of the root module — otherwise the import path is
#      claimed by both modules and consumers hit "ambiguous import". Pinning every
#      require to the version being released guarantees that by construction.
#
# Refuses a dirty tree (the tag must capture a known commit) and a VERSION that is
# already tagged. Local steps (pin, commit, tag) run automatically; the outward
# push is confirmed interactively, or printed for you to run when non-interactive.
#
#   just release v0.8.1
release VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    v='{{VERSION}}'
    semver='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
    [[ "$v" =~ $semver ]] || { echo "✗ VERSION must look like v1.2.3 or v1.2.3-rc.1 (got '$v')" >&2; exit 1; }

    git rev-parse --git-dir >/dev/null 2>&1 || { echo "✗ not a git repo" >&2; exit 1; }
    [ -z "$(git status --porcelain)" ] || { echo "✗ working tree not clean — commit or stash first (the release tag must capture a known commit):" >&2; git status --short >&2; exit 1; }

    # Publishable = module path is gosqlite.org or gosqlite.org/*. (xorm-compat is
    # module 'xormcompat', examples/liteorm is 'liteormexample' — neither matches,
    # so both are skipped automatically. Hidden dirs like .liteorm are excluded.)
    pub=()
    while IFS= read -r gomod; do
        mp=$(awk '/^module /{print $2; exit}' "$gomod")
        case "$mp" in gosqlite.org|gosqlite.org/*) pub+=("$(dirname "$gomod")") ;; esac
    done < <(find . -name go.mod -not -path './.*/*' | sort)
    [ "${#pub[@]}" -gt 0 ] || { echo "✗ no publishable gosqlite.org modules found" >&2; exit 1; }

    # The tag each module gets: 'vX.Y.Z' for the root, '<subdir>/vX.Y.Z' for a
    # nested module (Go resolves a subdir module ONLY from its path-prefixed tag).
    tags=()
    for d in "${pub[@]}"; do
        case "$d" in .) tags+=("$v") ;; *) tags+=("${d#./}/$v") ;; esac
    done
    for t in "${tags[@]}"; do
        git rev-parse -q --verify "refs/tags/$t" >/dev/null && { echo "✗ tag $t already exists — pick a new VERSION" >&2; exit 1; }
    done

    echo "→ publishable modules: ${pub[*]}"
    echo "→ pinning intra-repo gosqlite.org requires to $v"
    pinned=()
    for d in "${pub[@]}"; do
        # Each REQUIRED gosqlite.org* path (require lines read "<path> v<ver>"; the
        # 'module' line and '=>' replace lines carry no " v<ver>", so neither matches).
        for p in $(grep -oE "gosqlite\.org(/[A-Za-z0-9._/-]+)? v[0-9][^[:space:]]*" "$d/go.mod" | sed -E 's/ v.*//' | sort -u); do
            (cd "$d" && go mod edit -require="$p@$v"); echo "    $d: $p → $v"; pinned+=("$d/go.mod")
        done
    done
    [ "${#pinned[@]}" -gt 0 ] || echo "    (no intra-repo gosqlite.org requires to bump)"

    echo "→ verifying every publishable module still builds (replace directives resolve locally)"
    for d in "${pub[@]}"; do ( cd "$d" && go build ./... ); done

    committed=0
    if [ "${#pinned[@]}" -gt 0 ]; then
        git add "${pinned[@]}"
        if git diff --cached --quiet; then
            echo "→ requires already at $v; nothing to commit, tagging HEAD"
        else
            git commit -q -m "release $v: pin intra-repo gosqlite.org requires to $v"
            committed=1
            echo "→ committed the require pin"
        fi
    else
        echo "→ no intra-repo requires; tagging current HEAD"
    fi

    echo "→ tagging every module at $(git rev-parse --short HEAD)"
    for t in "${tags[@]}"; do git tag "$t"; echo "    $t"; done

    push="git push origin HEAD ${tags[*]}"
    echo
    do_push=0
    if [ -t 0 ]; then
        read -r -p "→ push commit + ${#tags[@]} tag(s) to origin now? [y/N] " ans || ans=""
        [[ "$ans" == [yY] ]] && do_push=1
    fi
    if [ "$do_push" -eq 1 ]; then
        $push
        echo "✓ released $v — consumers can 'go get' each module once the vanity meta resolves and the tags reach the proxy."
    else
        echo "→ local commit + tags created but NOT pushed. To publish, run:"
        echo "      $push"
        echo "  Or to abort and undo what this recipe just did locally:"
        echo "      git tag -d ${tags[*]}"
        [ "$committed" -eq 1 ] && echo "      git reset --hard HEAD~1"
    fi

# Pin every examples/* module's intra-repo gosqlite.org requires that are REPLACED
# to a local tree up to VERSION, so the require line reads the release version (the
# build still resolves through the replace). Example modules are not gosqlite.org/*,
# so `just release` never touches them; run this AFTER it. A gosqlite require that is
# NOT replaced locally is left alone — it resolves from a published tag and is pinned
# by its own module (e.g. a sibling project's dialect), not the example. go.sum is
# unchanged: replaced modules are not checksummed.
pin-examples VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    v='{{VERSION}}'
    [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "✗ VERSION must look like v1.2.3 (got '$v')" >&2; exit 1; }
    for gomod in $(find examples -name go.mod | sort); do
        d=$(dirname "$gomod")
        reqs=$(grep -oE 'gosqlite\.org(/[A-Za-z0-9._/-]+)? v[0-9]' "$gomod" | awk '{print $1}' | sort -u || true)
        repl=$(grep -oE '^replace gosqlite\.org(/[A-Za-z0-9._/-]+)? ' "$gomod" | awk '{print $2}' | sort -u || true)
        both=$(comm -12 <(echo "$reqs") <(echo "$repl") | sed '/^$/d')
        echo "→ $d"
        if [ -n "$both" ]; then
            for p in $both; do (cd "$d" && go mod edit -require="$p@$v"); echo "    pinned $p → $v"; done
        else
            echo "    (no locally-replaced gosqlite requires to pin)"
        fi
        left=$(comm -23 <(echo "$reqs") <(echo "$repl") | sed '/^$/d')
        [ -n "$left" ] && echo "$left" | sed 's/^/    left as-is (not replaced locally): /'
        # build into a throwaway dir so a single-main example does not drop a binary
        (cd "$d" && tmp=$(mktemp -d) && trap 'rm -rf "$tmp"' EXIT && go build -o "$tmp/" ./...) && echo "    ✓ builds"
    done
