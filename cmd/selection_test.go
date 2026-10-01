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
// answer otherwise than the whole suite, and the verdicts the whole suite gives their mutants.
var selectionCases = []struct {
	fixture string
	workers int
	want    map[string]mutator.Status
}{
	// TestA and TestC fail together without TestB between them.
	{"pollute", 0, map[string]mutator.Status{"pollute.go:23 ARITHMETIC_BASE": mutator.Lived}},
	// Only TestCheck checks the cached path, which it reaches only after TestPrime.
	{"warmcache", 0, map[string]mutator.Status{
		"warmcache.go:9 ARITHMETIC_BASE":  mutator.Killed,
		"warmcache.go:13 ARITHMETIC_BASE": mutator.Lived,
	}},
	// Indexing takes longer than a mutant's time: TestMain sleeps a second before each test.
	{"slowmain", 2, map[string]mutator.Status{
		"slowmain.go:6 CONDITIONALS_BOUNDARY": mutator.Lived,
		"slowmain.go:6 CONDITIONALS_NEGATION": mutator.Lived,
	}},
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
			dir := filepath.Join(t.TempDir(), tc.fixture)
			if err := os.CopyFS(dir, os.DirFS(filepath.Join(fixtures, tc.fixture))); err != nil {
				t.Fatal(err)
			}
			t.Chdir(".") // the coverage run changes to the module's directory
			mod, err := gomodule.Init(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(configuration.Reset)
			configuration.Set(configuration.UnleashTestSelectionKey, true)
			configuration.Set(configuration.UnleashSelectionConfirmKey, 5)
			configuration.Set(configuration.UnleashWorkersKey, tc.workers)
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
			if !maps.Equal(got, tc.want) {
				t.Errorf("verdicts %v, want %v", got, tc.want)
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
	dir := filepath.Join(t.TempDir(), "unbuilt")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "unbuilt"))); err != nil {
		t.Fatal(err)
	}
	mod, err := gomodule.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(".") // the coverage run changes to the module's directory
	t.Cleanup(configuration.Reset)
	configuration.Set(configuration.MutantTypeEnabledKey(mutator.ArithmeticBase), true)
	results, err := run(context.Background(), mod, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Mutants) != 1 || results.Mutants[0].Status() != mutator.NotViable {
		t.Fatalf("mutants %v", results.Mutants)
	}
}
