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
	"go/token"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/selection"
)

const (
	fileA = "a.go"
	pkgA  = "example.com/a"
)

// twoTests is an index of a.go: TestOne covers lines 3 to 5, TestTwo lines 4 to 9.
var twoTests = selection.Index{
	Tests: []string{"TestOne", "TestTwo"},
	Profiles: []coverage.Profile{
		{fileA: {{StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 2}}},
		{fileA: {{StartLine: 4, StartCol: 1, EndLine: 9, EndCol: 2}}},
	},
}

func TestThePatternNamesTheTestsThatCoverThePosition(t *testing.T) {
	cases := []struct {
		pos     token.Position
		pattern string
		ok      bool
	}{
		{token.Position{Filename: fileA, Line: 3, Column: 5}, "^(TestOne)$", true},
		{token.Position{Filename: fileA, Line: 4, Column: 5}, "^(TestOne|TestTwo)$", true},
		{token.Position{Filename: fileA, Line: 8, Column: 5}, "^(TestTwo)$", true},
		{token.Position{Filename: fileA, Line: 12, Column: 5}, "", false},
		{token.Position{Filename: "b.go", Line: 4, Column: 5}, "", false},
	}
	for _, tc := range cases {
		pattern, ok := twoTests.Pattern(tc.pos)
		if pattern != tc.pattern || ok != tc.ok {
			t.Errorf("%v: %q, %t; want %q, %t", tc.pos, pattern, ok, tc.pattern, tc.ok)
		}
	}
	if pattern, ok := (selection.Index{}).Pattern(token.Position{Filename: fileA, Line: 4}); pattern != "" || ok {
		t.Errorf("an empty index: %q, %t", pattern, ok)
	}
}

func TestATestNameIsMatchedLiterally(t *testing.T) {
	ix := selection.Index{
		Tests:    []string{"Test.Odd+"},
		Profiles: []coverage.Profile{{fileA: {{StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1}}}},
	}
	if pattern, _ := ix.Pattern(token.Position{Filename: fileA, Line: 1, Column: 3}); pattern != `^(Test\.Odd\+)$` {
		t.Errorf("pattern %q", pattern)
	}
}

// builderStub answers an index per package and counts its calls.
type builderStub struct {
	calls   atomic.Int32
	indexes map[string]selection.Index
	err     error
}

func (b *builderStub) Index(pkg string) (selection.Index, error) {
	b.calls.Add(1)

	return b.indexes[pkg], b.err
}

func TestAPackageIsIndexedOnceForAllItsMutants(t *testing.T) {
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: twoTests}}
	s := selection.New(builder, func(pkg string, err error) { t.Errorf("%s: %v", pkg, err) })
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pattern, ok := s.Pattern(pkgA, token.Position{Filename: fileA, Line: 8, Column: 1}); pattern != "^(TestTwo)$" || !ok {
				t.Errorf("pattern %q, %t", pattern, ok)
			}
		}()
	}
	wg.Wait()
	if got := builder.calls.Load(); got != 1 {
		t.Errorf("indexed %d times", got)
	}
	if _, ok := s.Pattern("example.com/b", token.Position{Filename: fileA, Line: 8, Column: 1}); ok || builder.calls.Load() != 2 {
		t.Errorf("another package: ok %t, %d indexings", ok, builder.calls.Load())
	}
}

func TestAPackageThatCannotBeIndexedRunsEveryTest(t *testing.T) {
	boom := errors.New("boom")
	builder := &builderStub{indexes: map[string]selection.Index{pkgA: twoTests}, err: boom}
	var heard []error
	s := selection.New(builder, func(pkg string, err error) {
		if pkg != pkgA {
			t.Errorf("heard of %s", pkg)
		}
		heard = append(heard, err)
	})
	for range 3 {
		if pattern, ok := s.Pattern(pkgA, token.Position{Filename: fileA, Line: 4, Column: 1}); pattern != "" || ok {
			t.Errorf("pattern %q, %t", pattern, ok)
		}
	}
	if len(heard) != 1 || !errors.Is(heard[0], boom) {
		t.Errorf("heard %v", heard)
	}
}
