// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package par

import (
	"math/rand"
	"sync"
)

// Work manages a set of work items to be executed in parallel, at most once each.
// The items in the set must all be valid map keys.
type Work[T comparable] struct {
	f       func(T) // function to run for each item
	running int     // total number of runners

	mu      sync.Mutex
	added   map[T]bool // items added to set
	todo    []T        // items yet to be run
	wait    sync.Cond  // wait when todo is empty
	waiting int        // number of runners waiting for todo
}

func (w *Work[T]) init() {
	if w.added == nil {
		w.added = make(map[T]bool)
	}
}

// Add adds item to the work set, if it hasn't already been added.
func (w *Work[T]) Add(item T) {
	w.mu.Lock()
	w.init()
	if !w.added[item] {
		w.added[item] = true
		w.todo = append(w.todo, item)
		if w.waiting > 0 {
			w.wait.Signal()
		}
	}
	w.mu.Unlock()
}

// Do runs f in parallel on items from the work set,
// with at most n invocations of f running at a time.
// It returns when everything added to the work set has been processed.
// At least one item should have been added to the work set
// before calling Do (or else Do returns immediately),
// but it is allowed for f(item) to add new items to the set.
// Do should only be used once on a given Work.
func (w *Work[T]) Do(n int, f func(item T)) {
	if n < 1 {
		panic("par.Work.Do: n < 1")
	}
	if w.running >= 1 {
		panic("par.Work.Do: already called Do")
	}

	w.running = n
	w.f = f
	w.wait.L = &w.mu

	for i := 0; i < n-1; i++ {
		go w.runner()
	}
	w.runner()
}

// runner executes work in w until both nothing is left to do
// and all the runners are waiting for work.
// (Then all the runners return.)
func (w *Work[T]) runner() {
	for {
		// Wait for something to do.
		w.mu.Lock()
		for len(w.todo) == 0 {
			w.waiting++
			if w.waiting == w.running {
				// All done.
				w.wait.Broadcast()
				w.mu.Unlock()
				return
			}
			w.wait.Wait()
			w.waiting--
		}

		// Pick something to do at random,
		// to eliminate pathological contention
		// in case items added at about the same time
		// are most likely to contend.
		i := rand.Intn(len(w.todo))
		item := w.todo[i]
		w.todo[i] = w.todo[len(w.todo)-1]
		w.todo = w.todo[:len(w.todo)-1]
		w.mu.Unlock()

		w.f(item)
	}
}
