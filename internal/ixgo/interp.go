// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ixgo

import (
	"sync"

	goixgo "github.com/goplus/ixgo"
)

var interpMu sync.Mutex

// LockInterp serializes operations that mutate ixgo's global reflectx state.
func LockInterp() {
	interpMu.Lock()
}

// UnlockInterp releases the interpreter lifecycle lock.
func UnlockInterp() {
	interpMu.Unlock()
}

// ReleaseInterp releases an interpreter after its Go owner becomes unreachable.
func ReleaseInterp(interp *goixgo.Interp) {
	interpMu.Lock()
	defer interpMu.Unlock()
	interp.UnsafeRelease()
}
