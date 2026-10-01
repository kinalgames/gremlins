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

// Package selection chooses, for each mutant, the tests of its package that execute the mutated
// position, so that a mutant runs those tests instead of the package's whole suite.
//
// The coverage of each test is gathered once per package: the package's test binary, built with
// coverage, runs each of its tests alone (-test.run '^Name$'). Each such run executes the
// package's initialisation and its TestMain, so a position that only package-level variables,
// init functions or TestMain reach is covered by every test, and every test runs for it.
//
// A mutant whose position no single test covers runs the whole suite: the package-level coverage
// decides what is NOT COVERED, never the selection. A package whose tests cannot be indexed (a
// build failure, a test that fails when it runs alone) runs the whole suite for every mutant.
//
// Selection assumes that a test's outcome does not depend on which tests ran before it in the same
// process. The engine guards a subset's verdict where that assumption can fail: a subset's kill,
// time-out or crash counts only if the same subset passes on the unmutated code; every survivor,
// a fixed sample of the kills and each package's lowest kill run again with the whole suite; and a
// package where the whole suite lets such a kill live runs the whole suite for the rest of the
// run, its other kills included. What remains is a mutant that itself leaves state behind for a later selected
// test, outside the confirmed kills: a test does fail on it, and the same tests pass together on
// the unmutated code.
package selection

import (
	"fmt"
	"go/token"
	"hash/fnv"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-gremlins/gremlins/internal/coverage"
)

// Index holds the coverage of each test of one package, with the package's unmutated test binary
// and the directory its tests run in.
type Index struct {
	Tests    []string
	Profiles []coverage.Profile
	Binary   string
	Dir      string
}

// MaxPattern bounds a -run pattern in bytes, well below the command lines Windows (32 767
// characters) and Linux (128 KiB per argument) accept: a longer selection runs every test.
const MaxPattern = 8192

// Pattern is the -run pattern of the tests that cover pos, in the order of the index; "" runs
// every test: when no test covers pos, when every test does, or when the pattern would pass
// MaxPattern bytes.
func (ix Index) Pattern(pos token.Position) string {
	var names []string
	for i, p := range ix.Profiles {
		if p.IsCovered(pos) {
			names = append(names, regexp.QuoteMeta(ix.Tests[i]))
		}
	}
	pattern := "^(" + strings.Join(names, "|") + ")$"
	if len(names) == 0 || len(names) == len(ix.Tests) || len(pattern) > MaxPattern {
		return ""
	}

	return pattern
}

// Builder indexes the tests of a package, by its import path, and runs a subset of them on the
// unmutated code.
type Builder interface {
	Index(pkg string) (Index, error)
	Passes(ix Index, pattern string) error
}

// Selector answers, for a mutant of a package, which of the package's tests to run, and keeps
// what the run learns of each package. It indexes each package once, on its first mutant; the
// mutants of a package wait for its index.
type Selector struct {
	builder Builder
	confirm int
	report  func(pkg string, err error)
	mu      sync.Mutex
	pkgs    map[string]*pkgState
	passes  map[string]*passState
}

type pkgState struct {
	once     sync.Once
	index    Index
	disabled atomic.Bool
}

type passState struct {
	once sync.Once
	err  error
}

// New is a Selector indexing packages with builder. confirmPercent of a package's subset kills
// are confirmed with the whole suite (Sampled). report hears of each package that could not be
// indexed, of each subset that fails on the unmutated code, and of each package whose confirmation
// disagreed; each of those runs the whole suite where it would have run the subset.
func New(builder Builder, confirmPercent int, report func(pkg string, err error)) *Selector {
	return &Selector{
		builder: builder, confirm: max(confirmPercent, 0), report: report,
		pkgs: map[string]*pkgState{}, passes: map[string]*passState{},
	}
}

// entry is the package's state, not indexed yet unless state indexed it.
func (s *Selector) entry(pkg string) *pkgState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, found := s.pkgs[pkg]
	if !found {
		st = &pkgState{}
		s.pkgs[pkg] = st
	}

	return st
}

// state is the package's state, indexed on first use.
func (s *Selector) state(pkg string) *pkgState {
	st := s.entry(pkg)
	st.once.Do(func() {
		index, err := s.builder.Index(pkg)
		if err != nil {
			s.report(pkg, fmt.Errorf("its tests could not be indexed, every test runs for each of its mutants: %w", err))
			index = Index{}
		}
		st.index = index
	})

	return st
}

// Pattern is the -run pattern of the tests of pkg that cover pos; "" runs every test of pkg.
func (s *Selector) Pattern(pkg string, pos token.Position) string {
	if s.Disabled(pkg) {
		return ""
	}

	return s.state(pkg).index.Pattern(pos)
}

// Passes reports whether the tests pattern selects in pkg pass together on the unmutated code,
// running them once per package and pattern. A subset that fails there would kill every mutant it
// runs for.
func (s *Selector) Passes(pkg, pattern string) bool {
	st := s.state(pkg)
	s.mu.Lock()
	p, found := s.passes[pkg+"\x00"+pattern]
	if !found {
		p = &passState{}
		s.passes[pkg+"\x00"+pattern] = p
	}
	s.mu.Unlock()
	p.once.Do(func() {
		if p.err = s.builder.Passes(st.index, pattern); p.err != nil {
			s.report(pkg, fmt.Errorf("the tests %s fail together on the unmutated code, so the mutants they kill run every test: %w", pattern, p.err))
		}
	})

	return p.err == nil
}

// Sampled reports whether the subset kill of the mutant id in pkg is to be confirmed with the
// whole suite now: every kill of a disabled package, and the kills whose id hashes into the
// confirmed percentage, the same mutants on every run.
func (s *Selector) Sampled(pkg, id string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))

	return s.Disabled(pkg) || int(h.Sum32()%100) < s.confirm
}

// Disabled reports whether every later mutant of pkg runs every test (Disable). It does not index
// the package.
func (s *Selector) Disabled(pkg string) bool {
	return s.entry(pkg).disabled.Load()
}

// Restore makes every later mutant of pkg run every test, as an earlier run that disabled pkg
// already reported. It does not index the package.
func (s *Selector) Restore(pkg string) {
	s.entry(pkg).disabled.Store(true)
}

// Disable makes every later mutant of pkg run every test, for the reason err. It does not index
// the package.
func (s *Selector) Disable(pkg string, err error) {
	if !s.entry(pkg).disabled.Swap(true) {
		s.report(pkg, fmt.Errorf("SELECTION DISAGREES WITH THE WHOLE SUITE, every test runs for its remaining mutants: %w", err))
	}
}
