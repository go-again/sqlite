// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

//go:build race

package sqlite

// raceEnabled = true in -race builds. See race_helper_test.go for the
// non-race counterpart and the rationale for the flag.
const raceEnabled = true
