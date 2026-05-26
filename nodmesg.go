// Copyright 2023 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

//go:build !sqlite.dmesg

package sqlite // import "github.com/go-again/sqlite"

const dmesgs = false

func dmesg(s string, args ...any) {}
