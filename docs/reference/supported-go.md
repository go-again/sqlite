---
title: Supported Go versions
description: The support policy — the two most recent Go releases — and where the real pin lives.
sidebar:
  order: 27
---

# Supported Go versions

The package supports the **two most recent Go releases**. Older Go is out of support on purpose; when a new minor ships, the just-superseded release is dropped within one cycle. The authoritative pin is the `go` directive in [`go.mod`](../../go.mod) — this page states the policy, not a version number.

Modern syntax is treated as a feature, not a hazard: generics, `iter.Seq2`, `log/slog`, generic type aliases, range-over-int, `sync.WaitGroup.Go`, `strings.SplitSeq`, and `reflect.TypeFor` are all used freely, and `gopls modernize` is enforced in CI.

If you need to build on an older Go, pin an older release of this module — but the support window only covers the two-release policy above.
