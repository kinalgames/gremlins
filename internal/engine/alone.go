/*
 * Copyright 2026 The Gremlins Authors
 *
 *    Licensed under the Apache License, Version 2.0 (the "License");
 *    you may not use this file except in compliance with the License.
 *    You may obtain a copy of the License at
 *
 *        http://www.apache.org/licenses/LICENSE-2.0
 *
 *    Unless required by applicable law or agreed to in writing, software
 *    distributed under the License is distributed on an "AS IS" BASIS,
 *    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *    See the License for the specific language governing permissions and
 *    limitations under the License.
 */

package engine

import (
	"sync"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// AloneRunner is an ExecutorDealer that runs a mutant again, alone, once every other mutant has
// run: a mutant whose run timed out, crashed or could not be carried out, whose outcome may come
// from the load it ran under. Its verdict stands only when it repeats alone.
type AloneRunner interface {
	// Again reports whether mut runs again alone.
	Again(mut mutator.Mutator) bool
	// RunAlone runs mut again, with every test of its package, and sets its verdict.
	RunAlone(mut mutator.Mutator)
}

// firsts are the outcomes of the mutants that run again alone.
type firsts struct {
	mu sync.Mutex
	of map[mutator.Mutator]outcome
}

func (f *firsts) put(mut mutator.Mutator, o outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.of[mut] = o
}

func (f *firsts) get(mut mutator.Mutator) (outcome, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, found := f.of[mut]

	return o, found
}

// aloneWorker names the work directory of the mutants that run again alone.
const aloneWorker = "alone"

// Again reports whether mut's run ended so that it runs again alone.
func (m MutantExecutorDealer) Again(mut mutator.Mutator) bool {
	_, found := m.firsts.get(mut)

	return found
}

// RunAlone runs mut again with every test of its package, in a work directory of its own, and
// gives it the verdict of its two outcomes (aloneStatus). The engine calls it once no other mutant
// runs.
func (m MutantExecutorDealer) RunAlone(mut mutator.Mutator) {
	first, _ := m.firsts.get(mut)
	e := m.executor(mut, nil, nil)
	rootDir := e.workdir(aloneWorker)
	mut.SetStatus(aloneStatus(first, e.test(func() outcome { return e.runSuite(rootDir, mut.Pkg(), "") })))
}
