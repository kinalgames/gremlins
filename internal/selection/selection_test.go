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

package selection_test

import (
	"errors"
	"fmt"
	"go/token"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/selection"
)

const (
	fileA = "a.go"
	pkgA  = "example.com/a"
	pkgB  = "example.com/b"

	testOne = "^(TestOne)$"
)

// threeTests is an index of a.go: TestOne covers lines 3 to 5, TestTwo lines 4 to 9 and
// TestThree lines 4 to 20.
var threeTests = selection.Index{
	Tests: []string{"TestOne", "TestTwo", "TestThree"},
	Profiles: []coverage.Profile{
		{fileA: {{StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 2}}},
		{fileA: {{StartLine: 4, StartCol: 1, EndLine: 9, EndCol: 2}}},
		{fileA: {{StartLine: 4, StartCol: 1, EndLine: 20, EndCol: 2}}},
	},
}

func at(line int) token.Position { return token.Position{Filename: fileA, Line: line, Column: 5} }

func TestThePatternNamesTheTestsThatCoverThePosition(t *testing.T) {
	cases := []struct {
		pos     token.Position
		pattern string
	}{
		{at(3), testOne},
		{at(8), "^(TestTwo|TestThree)$"},
		{at(15), "^(TestThree)$"},
		{at(4), ""},  // every test: the whole suite runs, with no -run
		{at(25), ""}, // no test: the whole suite runs, and the package's coverage decides
		{token.Position{Filename: "b.go", Line: 4, Column: 5}, ""},
	}
	for _, tc := range cases {
		if pattern := threeTests.Pattern(tc.pos); pattern != tc.pattern {
			t.Errorf("%v: %q, want %q", tc.pos, pattern, tc.pattern)
		}
	}
	if pattern := (selection.Index{}).Pattern(at(4)); pattern != "" {
		t.Errorf("an empty index: %q", pattern)
	}
}

func TestATestNameIsMatchedLiterally(t *testing.T) {
	ix := selection.Index{
		Tests:    []string{"Test.Odd+", "TestOther"},
		Profiles: []coverage.Profile{{fileA: {{StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1}}}, {}},
	}
	if pattern := ix.Pattern(at(1)); pattern != `^(Test\.Odd\+)$` {
		t.Errorf("pattern %q", pattern)
	}
}

// longName is an index whose one covering test has a name of n bytes, beside a test that covers
// nothing: its pattern, ^(name)$, has n+4 bytes.
func longName(n int) selection.Index {
	return selection.Index{
		Tests:    []string{"Test" + strings.Repeat("x", n-4), "TestOther"},
		Profiles: []coverage.Profile{{fileA: {{StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1}}}, {}},
	}
}

func TestAPatternLongerThanTheBoundRunsEveryTest(t *testing.T) {
	if pattern := longName(selection.MaxPattern - 4).Pattern(at(1)); len(pattern) != selection.MaxPattern {
		t.Errorf("a pattern of the bound's length: %d bytes", len(pattern))
	}
	if pattern := longName(selection.MaxPattern - 3).Pattern(at(1)); pattern != "" {
		t.Errorf("a pattern past the bound: %d bytes", len(pattern))
	}
}

// builderStub answers an index per package, counts its calls, and fails Passes for the patterns
// in failing.
type builderStub struct {
	calls   atomic.Int32
	passes  atomic.Int32
	indexes map[string]selection.Index
	failing map[string]bool
	err     error
}

func (b *builderStub) Index(pkg string) (selection.Index, error) {
	b.calls.Add(1)

	return b.indexes[pkg], b.err
}

func (b *builderStub) Passes(_ selection.Index, pattern string) error {
	b.passes.Add(1)
	if b.failing[pattern] {
		return errors.New("FAIL")
	}

	return nil
}

// heard collects what a selector reports.
type heard struct {
	m    sync.Mutex
	errs []string
}

func (h *heard) report(pkg string, err error) {
	h.m.Lock()
	defer h.m.Unlock()
	h.errs = append(h.errs, pkg+": "+err.Error())
}

func (h *heard) all() []string {
	h.m.Lock()
	defer h.m.Unlock()

	return append([]string(nil), h.errs...)
}

func TestAPackageIsIndexedOnceForAllItsMutants(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: threeTests}}
	h := &heard{}
	s := selection.New(builder, 0, h.report)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pattern := s.Pattern(pkgA, at(15)); pattern != "^(TestThree)$" {
				t.Errorf("pattern %q", pattern)
			}
		}()
	}
	wg.Wait()
	if got := builder.calls.Load(); got != 1 {
		t.Errorf("indexed %d times", got)
	}
	if pattern := s.Pattern(pkgB, at(15)); pattern != "" || builder.calls.Load() != 2 {
		t.Errorf("another package: %q, %d indexings", pattern, builder.calls.Load())
	}
	if got := h.all(); len(got) != 0 {
		t.Errorf("heard %v", got)
	}
}

func TestAPackageThatCannotBeIndexedRunsEveryTest(t *testing.T) {
	boom := errors.New("boom")
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: threeTests}, err: boom}
	h := &heard{}
	s := selection.New(builder, 0, h.report)
	for range 3 {
		if pattern := s.Pattern(pkgA, at(3)); pattern != "" {
			t.Errorf("pattern %q", pattern)
		}
	}
	if got := h.all(); len(got) != 1 || !strings.HasPrefix(got[0], pkgA+": ") || !strings.HasSuffix(got[0], "boom") {
		t.Errorf("heard %v", got)
	}
}

func TestASubsetRunsOnTheUnmutatedCodeOncePerPackageAndPattern(t *testing.T) {
	builder := &builderStub{
		indexes: map[string]selection.Index{pkgA: threeTests, pkgB: threeTests},
		failing: map[string]bool{testOne: true},
	}
	h := &heard{}
	s := selection.New(builder, 0, h.report)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Passes(pkgA, testOne) {
				t.Error("a failing subset passes")
			}
			if !s.Passes(pkgA, "^(TestTwo)$") {
				t.Error("a passing subset fails")
			}
		}()
	}
	wg.Wait()
	if s.Passes(pkgB, testOne) || builder.passes.Load() != 3 {
		t.Errorf("another package: %d runs", builder.passes.Load())
	}
	got := h.all()
	if len(got) != 2 || !strings.Contains(got[0], "^(TestOne)$ fail together") || !strings.HasSuffix(got[0], "FAIL") {
		t.Errorf("heard %v", got)
	}
}

// sampled counts the kills, of ids "m0" to "m<n-1>" in a fresh package, that s confirms.
func sampled(s *selection.Selector, pkg string, n int) int {
	count := 0
	for i := range n {
		if s.Sampled(pkg, fmt.Sprintf("m%d", i)) {
			count++
		}
	}

	return count
}

func TestTheConfirmedShareOfKillsIsSampled(t *testing.T) {
	cases := []struct {
		percent  int
		min, max int
	}{
		{-5, 0, 0},
		{0, 0, 0},
		{5, 400, 600},
		{100, 10000, 10000},
	}
	for _, tc := range cases {
		builder := &builderStub{indexes: map[string]selection.Index{}}
		got := sampled(selection.New(builder, tc.percent, (&heard{}).report), pkgA, 10000)
		if got < tc.min || got > tc.max {
			t.Errorf("%d %%: %d of 10000 sampled, want %d to %d", tc.percent, got, tc.min, tc.max)
		}
	}
}

func TestTheSampleIsTheSameOnEveryRunWhateverTheOrder(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{}}
	first, second := selection.New(builder, 50, (&heard{}).report), selection.New(builder, 50, (&heard{}).report)
	for i := range 200 {
		id, reverse := fmt.Sprintf("m%d", i), fmt.Sprintf("m%d", 199-i)
		if first.Sampled(pkgA, id) != second.Sampled(pkgA, id) || first.Sampled(pkgA, reverse) != second.Sampled(pkgA, reverse) {
			t.Fatalf("%s or %s is sampled on one run only", id, reverse)
		}
	}
}

func TestEveryKillOfADisabledPackageIsSampled(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{}}
	s := selection.New(builder, 0, (&heard{}).report)
	if s.Disabled(pkgA) || s.Sampled(pkgA, "m1") {
		t.Fatal("sampled before the package was disabled")
	}
	s.Disable(pkgA, errors.New("disagreed"))
	if !s.Disabled(pkgA) || sampled(s, pkgA, 100) != 100 || s.Disabled(pkgB) || sampled(s, pkgB, 100) != 0 {
		t.Fatal("a disabled package's kills are not all sampled, or another package's are")
	}
	if got := builder.calls.Load(); got != 0 { // a run that settles deferred mutants indexes nothing
		t.Fatalf("disabling and sampling indexed %d times", got)
	}
}

func TestADisabledPackageRunsEveryTest(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: threeTests, pkgB: threeTests}}
	h := &heard{}
	s := selection.New(builder, 0, h.report)
	s.Disable(pkgA, errors.New("disagreed"))
	s.Disable(pkgA, errors.New("disagreed again"))
	if pattern := s.Pattern(pkgA, at(3)); pattern != "" {
		t.Errorf("disabled: %q", pattern)
	}
	if pattern := s.Pattern(pkgB, at(3)); pattern != testOne {
		t.Errorf("another package: %q", pattern)
	}
	if got := h.all(); len(got) != 1 || !strings.Contains(got[0], "SELECTION DISAGREES WITH THE WHOLE SUITE") || !strings.HasSuffix(got[0], "disagreed") {
		t.Errorf("heard %v", got)
	}
}

func TestARestoredPackageRunsEveryTestWithoutReportingItAgain(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: threeTests, pkgB: threeTests}}
	h := &heard{}
	s := selection.New(builder, 0, h.report)
	s.Restore(pkgA)
	if !s.Disabled(pkgA) || s.Disabled(pkgB) {
		t.Fatal("Restore did not disable the package it names, or disabled another")
	}
	if got := builder.calls.Load(); got != 0 {
		t.Fatalf("restoring indexed %d times", got)
	}
	if pattern := s.Pattern(pkgA, at(3)); pattern != "" {
		t.Errorf("restored: %q", pattern)
	}
	s.Disable(pkgA, errors.New("disagreed"))
	if got := h.all(); len(got) != 0 {
		t.Errorf("heard %v, want nothing: the earlier run reported the package", got)
	}
}
