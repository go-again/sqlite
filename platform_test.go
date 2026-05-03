// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import "runtime"

// isDarwin is true when building/running on macOS. Used by tests to skip
// behaviors that depend on libc shims not yet implemented for darwin in
// modernc.org/libc (e.g. dlopen).
var isDarwin = runtime.GOOS == "darwin"
