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
// init functions or TestMain reach is covered by every test, and every test is selected for it.
//
// A mutant whose position no single test covers runs the whole suite: the package-level coverage
// decides what is NOT COVERED, never the selection. A package whose tests cannot be indexed (a
// build failure, a test that fails when it runs alone) runs the whole suite for every mutant.
package selection

import (
	"go/token"
	"regexp"
	"strings"
	"sync"

	"github.com/go-gremlins/gremlins/internal/coverage"
)

// Index holds the coverage of each test of one package.
type Index struct {
	Tests    []string
	Profiles []coverage.Profile
}

// Pattern is the -run pattern of the tests that cover pos, in the order of the index. ok is
// false when no test covers pos: the caller runs every test.
func (ix Index) Pattern(pos token.Position) (string, bool) {
	var names []string
	for i, p := range ix.Profiles {
		if p.IsCovered(pos) {
			names = append(names, regexp.QuoteMeta(ix.Tests[i]))
		}
	}
	if len(names) == 0 {
		return "", false
	}

	return "^(" + strings.Join(names, "|") + ")$", true
}

// Builder indexes the tests of a package, by its import path.
type Builder interface {
	Index(pkg string) (Index, error)
}

// Selector answers, for a mutant of a package, which of the package's tests to run. It indexes
// each package once, on its first mutant; the mutants of a package wait for its index.
type Selector struct {
	builder Builder
	onError func(pkg string, err error)
	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	once  sync.Once
	index Index
}

// New is a Selector indexing packages with builder; onError hears of each package that could
// not be indexed, whose mutants then run every test.
func New(builder Builder, onError func(pkg string, err error)) *Selector {
	return &Selector{builder: builder, onError: onError, entries: map[string]*entry{}}
}

// Pattern is the -run pattern of the tests of pkg that cover pos; ok is false when every test of
// pkg must run.
func (s *Selector) Pattern(pkg string, pos token.Position) (string, bool) {
	s.mu.Lock()
	e, found := s.entries[pkg]
	if !found {
		e = &entry{}
		s.entries[pkg] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		index, err := s.builder.Index(pkg)
		if err != nil {
			s.onError(pkg, err)
			index = Index{}
		}
		e.index = index
	})

	return e.index.Pattern(pos)
}
