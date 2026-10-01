/*
 * Copyright 2022 The Gremlins Authors
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

package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// selectionCases are fixtures of internal/selection/testdata where a subset of the tests can
// answer otherwise than the whole suite, and their verdicts with test selection and without it.
var selectionCases = []struct {
	fixture string
	workers int
	with    map[string]mutator.Status
	without map[string]mutator.Status
}{
	// TestA and TestC fail together without TestB between them.
	{"pollute", 0,
		map[string]mutator.Status{"pollute.go:23 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"pollute.go:23 ARITHMETIC_BASE": mutator.Lived}},
	// Only TestCheck checks the cached path, which it reaches only after TestPrime.
	{"warmcache", 0,
		map[string]mutator.Status{"warmcache.go:9 ARITHMETIC_BASE": mutator.Killed, "warmcache.go:13 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"warmcache.go:9 ARITHMETIC_BASE": mutator.Killed, "warmcache.go:13 ARITHMETIC_BASE": mutator.Lived}},
	// Indexing takes longer than a mutant's time: TestMain sleeps a second before each test.
	{"slowmain", 2,
		map[string]mutator.Status{"slowmain.go:6 CONDITIONALS_BOUNDARY": mutator.Lived, "slowmain.go:6 CONDITIONALS_NEGATION": mutator.Lived},
		map[string]mutator.Status{"slowmain.go:6 CONDITIONALS_BOUNDARY": mutator.Lived, "slowmain.go:6 CONDITIONALS_NEGATION": mutator.Lived}},
	// TestA and TestC hang together without TestB between them.
	{"hang", 0,
		map[string]mutator.Status{"hang.go:21 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"hang.go:21 ARITHMETIC_BASE": mutator.Lived}},
	// The accepted residual case: the mutant of Add leaves the total negative for TestC, which
	// TestB would have reset; TestA and TestC pass together on the unmutated code, and the mutant
	// is neither the package's lowest kill nor in the 5 % sample. With selection it is KILLED, as a
	// test does fail on it; the whole suite lets it live.
	{"residual", 0,
		map[string]mutator.Status{"residual.go:7 ARITHMETIC_BASE": mutator.Killed, "residual.go:11 ARITHMETIC_BASE": mutator.Killed},
		map[string]mutator.Status{"residual.go:7 ARITHMETIC_BASE": mutator.Killed, "residual.go:11 ARITHMETIC_BASE": mutator.Lived}},
}

// unleash runs gremlins with the go command on a copy of the module in dir, with the settings
// and the arithmetic and conditional mutants, and answers each mutant's verdict by
// "file:line TYPE".
func unleash(t *testing.T, dir string, settings map[string]any) map[string]mutator.Status {
	t.Helper()
	copied := filepath.Join(t.TempDir(), filepath.Base(dir))
	if err := os.CopyFS(copied, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	mod, err := gomodule.Init(copied)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // the coverage run changes to the module's directory
		if err := os.Chdir(wd); err != nil {
			t.Error(err)
		}
	}()
	defer configuration.Reset()
	for k, v := range settings {
		configuration.Set(k, v)
	}
	for _, mt := range []mutator.Type{mutator.ArithmeticBase, mutator.ConditionalsBoundary, mutator.ConditionalsNegation} {
		configuration.Set(configuration.MutantTypeEnabledKey(mt), true)
	}
	results, err := run(context.Background(), mod, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]mutator.Status{}
	for _, m := range results.Mutants {
		got[fmt.Sprintf("%s:%d %s", filepath.Base(m.Position().Filename), m.Position().Line, m.Type())] = m.Status()
	}

	return got
}

func TestTestSelectionKeepsTheWholeSuitesVerdicts(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	fixtures, err := filepath.Abs(filepath.Join("..", "internal", "selection", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range selectionCases {
		t.Run(tc.fixture, func(t *testing.T) {
			dir := filepath.Join(fixtures, tc.fixture)
			with := unleash(t, dir, map[string]any{
				configuration.UnleashTestSelectionKey:    true,
				configuration.UnleashSelectionConfirmKey: 5,
				configuration.UnleashWorkersKey:          tc.workers,
			})
			if !maps.Equal(with, tc.with) {
				t.Errorf("with selection: %v, want %v", with, tc.with)
			}
			if without := unleash(t, dir, map[string]any{configuration.UnleashWorkersKey: tc.workers}); !maps.Equal(without, tc.without) {
				t.Errorf("without selection: %v, want %v", without, tc.without)
			}
		})
	}
}

func TestAMutantThatDoesNotBuildIsNotViable(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	got := unleash(t, filepath.Join("testdata", "unbuilt"), nil)
	if want := map[string]mutator.Status{"unbuilt.go:6 ARITHMETIC_BASE": mutator.NotViable}; !maps.Equal(got, want) {
		t.Fatalf("verdicts %v, want %v", got, want)
	}
}
