// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec

// Importing modernc.org/sqlite/vec for side-effects auto-registers the
// sqlite-vec extension on every new database connection via
// sqlite3_auto_extension. This is what makes `CREATE VIRTUAL TABLE x USING
// vec0(...)` work without any per-connection setup.
import _ "modernc.org/sqlite/vec"
