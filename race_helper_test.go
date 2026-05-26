// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

//go:build !race

package sqlite

// raceEnabled is false in non-race builds; the companion file
// race_helper_race_test.go sets it to true when -race is in effect.
//
// Tests use this to skip cases that touch modernc-transpiled code paths
// known to trip Go's checkptr analyzer (which -race enables). LoadExtension
// is the canonical example — modernc's _sqlite3LoadExtension does pointer
// arithmetic that fails checkptr, even though the underlying C code is
// correct.
const raceEnabled = false
