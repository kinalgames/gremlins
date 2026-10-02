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
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// Settler is an ExecutorDealer some of whose mutants' verdicts may still change once every mutant
// has run: a time-out or a crash runs again alone, and a selection subset's kill that the whole
// suite has not confirmed is confirmed for the package's lowest kill and for every kill of a
// package whose confirmation disagreed.
type Settler interface {
	// Pending reports whether mut's verdict waits for Settle.
	Pending(mut mutator.Mutator) bool
	// Settle gives each of muts, all pending, its verdict, running them one at a time; a run
	// cancelled before a mutant that needs a run leaves that mutant UNCLASSIFIED.
	Settle(ctx context.Context, muts []mutator.Mutator)
}

// subsetKill is a mutant a selection subset killed in pkg; confirmed says the whole suite killed
// it too, recheck that the whole suite ran on it and neither passed nor failed, so Settle confirms
// it.
type subsetKill struct {
	pkg       string
	confirmed bool
	recheck   bool
}

// unsettled is what Settle needs of a run: the outcomes that run again alone and the subset kills.
type unsettled struct {
	mu    sync.Mutex
	first map[mutator.Mutator]outcome
	kills map[mutator.Mutator]subsetKill
	// resumed says the kills are those a run deferred (Defer), whose confirmations are the kills to
	// recheck alone: that run knew each package's lowest kill.
	resumed bool
}

func newUnsettled() *unsettled {
	return &unsettled{first: map[mutator.Mutator]outcome{}, kills: map[mutator.Mutator]subsetKill{}}
}

func (u *unsettled) runAgain(mut mutator.Mutator, o outcome) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.first[mut] = o
}

func (u *unsettled) kill(mut mutator.Mutator, k subsetKill) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.kills[mut] = k
}

// pending reports whether mut runs again alone or is an unconfirmed subset kill.
func (u *unsettled) pending(mut mutator.Mutator) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, again := u.first[mut]
	k, killed := u.kills[mut]

	return again || (killed && !k.confirmed)
}

// confirmations are, in id order, the subset kills Settle confirms with every test before any
// other mutant: the lowest subset kill of each package when the whole suite has not confirmed it,
// and every kill to recheck.
func (u *unsettled) confirmations() []mutator.Mutator {
	u.mu.Lock()
	defer u.mu.Unlock()
	lowest := map[string]mutator.Mutator{}
	var muts []mutator.Mutator
	for mut, k := range u.kills {
		if l, found := lowest[k.pkg]; !found || byID(mut, l) < 0 {
			lowest[k.pkg] = mut
		}
		if k.recheck {
			muts = append(muts, mut)
		}
	}
	for _, mut := range lowest {
		if k := u.kills[mut]; !u.resumed && !k.confirmed && !k.recheck {
			muts = append(muts, mut)
		}
	}
	slices.SortFunc(muts, byID)

	return muts
}

// mutantID names a mutant by its position and type, the same on every run.
func mutantID(mut mutator.Mutator) string {
	pos := mut.Position()

	return fmt.Sprintf("%s:%d:%d %s", pos.Filename, pos.Line, pos.Column, mut.Type())
}

// byID orders mutants by file, line, column and type: the lowest kill of a package is the same
// on every run.
func byID(a, b mutator.Mutator) int {
	pa, pb := a.Position(), b.Position()

	return cmp.Or(
		cmp.Compare(pa.Filename, pb.Filename),
		cmp.Compare(pa.Line, pb.Line),
		cmp.Compare(pa.Column, pb.Column),
		cmp.Compare(a.Type(), b.Type()),
	)
}

// aloneWorker names the work directory of the mutants that run alone.
const aloneWorker = "alone"

// Pending reports whether mut's verdict waits for Settle.
func (m MutantExecutorDealer) Pending(mut mutator.Mutator) bool {
	return m.unsettled.pending(mut)
}

// Settle gives the pending mutants their verdicts, one at a time and in id order, so that the
// verdicts do not depend on which mutant ended first: it confirms each package's lowest subset
// kill, and each kill to recheck, with every test, disabling the package's selection when every
// test lets the mutant live; it runs each time-out and crash again alone; and it confirms every
// other subset kill of a disabled package.
func (m MutantExecutorDealer) Settle(ctx context.Context, muts []mutator.Mutator) {
	settled := map[mutator.Mutator]bool{}
	for _, mut := range m.unsettled.confirmations() {
		settled[mut] = true
		if m.runAlone(ctx, mut, failed) == passed {
			m.selector.Disable(mut.Pkg(), fmt.Errorf("the selected tests killed the mutant %s, every test let it live", mutantID(mut)))
		}
	}
	muts = slices.Clone(muts)
	slices.SortFunc(muts, byID)
	for _, mut := range muts {
		m.unsettled.mu.Lock()
		first, again := m.unsettled.first[mut]
		k, killed := m.unsettled.kills[mut]
		m.unsettled.mu.Unlock()
		switch {
		case settled[mut]:
		case again:
			m.runAlone(ctx, mut, first)
		case killed && m.selector.Disabled(k.pkg):
			m.runAlone(ctx, mut, failed)
		}
	}
}

// runAlone runs mut again with every test of its package, in a work directory of its own, and
// gives it the verdict of its first outcome and this run's (aloneStatus); it reports the outcome.
// Every kill alone, a time-out or a crash that repeats or a test that fails, counts only if every
// test passes on the unmutated code in the same place: what failed may be the place, such as a
// database that went away, not the mutant. A cancelled run, or a copy that does not hold the
// source, leaves mut UNCLASSIFIED.
func (m MutantExecutorDealer) runAlone(ctx context.Context, mut mutator.Mutator, first outcome) outcome {
	if !checkDone(ctx) {
		mut.SetStatus(mutator.Unclassified)

		return broken
	}
	e := m.executor(mut, nil, nil)
	rootDir := e.workdir(aloneWorker)
	if !e.holdsSource(aloneWorker) {
		mut.SetStatus(mutator.Unclassified)

		return broken
	}
	o := e.test(aloneWorker, func() outcome { return e.runSuite(rootDir, mut.Pkg(), "") })
	status := aloneStatus(first, o)
	if repeats(first, o) || o == failed {
		if base := e.unmutated(); base != passed {
			log.Errorf("the mutant at %s %s, and every test %s on the unmutated code there, so it is UNCLASSIFIED\n", mut.Position(), aloneEnd(first, o), base)
			status = mutator.Unclassified
		}
	}
	mut.SetStatus(status)

	return o
}

// aloneEnd says how a run alone ended alone after a first run that ended first: "timed out again
// alone" or "crashed again alone" for a repeat, else such as "failed a test alone".
func aloneEnd(first, alone outcome) string {
	if repeats(first, alone) {
		return alone.String() + " again alone"
	}

	return alone.String() + " alone"
}

// unmutated is how every test of the mutant's package ends on the unmutated code in the copy of
// the mutants that run alone, with the mutant's arguments and time; broken when that copy does not
// hold the source.
func (m *mutantExecutor) unmutated() outcome {
	if m.copies.reason(aloneWorker) != nil {
		return broken
	}

	return m.runSuite(m.workdir(aloneWorker), m.mutant.Pkg(), "")
}
