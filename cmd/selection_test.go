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
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// selectionCases are fixtures of internal/selection/testdata where a subset of the tests can
// answer otherwise than the whole suite, and their verdicts with test selection and without it.
var selectionCases = []struct {
	fixture string
	workers int
	with    map[string]mutator.Status
	without map[string]mutator.Status
	heard   string // in what selection logs
}{
	// TestA and TestC fail together without TestB between them.
	{"pollute", 0,
		map[string]mutator.Status{"pollute.go:23 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"pollute.go:23 ARITHMETIC_BASE": mutator.Lived}, ""},
	// Only TestCheck checks the cached path, which it reaches only after TestPrime.
	{"warmcache", 0,
		map[string]mutator.Status{"warmcache.go:9 ARITHMETIC_BASE": mutator.Killed, "warmcache.go:13 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"warmcache.go:9 ARITHMETIC_BASE": mutator.Killed, "warmcache.go:13 ARITHMETIC_BASE": mutator.Lived}, ""},
	// Indexing takes longer than a mutant's time: TestMain sleeps a second before each test.
	{"slowmain", 2,
		map[string]mutator.Status{"slowmain.go:6 CONDITIONALS_BOUNDARY": mutator.Lived, "slowmain.go:6 CONDITIONALS_NEGATION": mutator.Lived},
		map[string]mutator.Status{"slowmain.go:6 CONDITIONALS_BOUNDARY": mutator.Lived, "slowmain.go:6 CONDITIONALS_NEGATION": mutator.Lived}, ""},
	// TestA and TestC hang together without TestB between them.
	{"hang", 0,
		map[string]mutator.Status{"hang.go:21 ARITHMETIC_BASE": mutator.Lived},
		map[string]mutator.Status{"hang.go:21 ARITHMETIC_BASE": mutator.Lived},
		"fail together"},
	// The accepted residual case: the mutant of Add leaves the total negative for TestC, which
	// TestB would have reset; TestA and TestC pass together on the unmutated code, and the mutant
	// is neither the package's lowest kill nor in the 5 % sample. With selection it is KILLED, as a
	// test does fail on it; the whole suite lets it live.
	{"residual", 0,
		map[string]mutator.Status{"residual.go:7 ARITHMETIC_BASE": mutator.Killed, "residual.go:11 ARITHMETIC_BASE": mutator.Killed},
		map[string]mutator.Status{"residual.go:7 ARITHMETIC_BASE": mutator.Killed, "residual.go:11 ARITHMETIC_BASE": mutator.Lived}, ""},
}

// unleash runs gremlins with the go command on a copy of the module in dir, with the settings
// and the arithmetic and conditional mutants, and answers each mutant's verdict by
// "file:line TYPE" and what it logged as errors. beside are files written beside the copy, by
// their path from its parent directory: the source has them, the copies gremlins runs in do not.
func unleash(t *testing.T, dir string, settings map[string]any, beside map[string]string) (map[string]mutator.Status, string) {
	t.Helper()
	parent := t.TempDir()
	copied := filepath.Join(parent, filepath.Base(dir))
	if err := os.CopyFS(copied, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	for name, content := range beside {
		file := filepath.Join(parent, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	errs := &bytes.Buffer{}
	log.Reset()
	log.Init(&bytes.Buffer{}, errs)
	defer log.Reset()
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

	return got, errs.String()
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
			with, heard := unleash(t, dir, map[string]any{
				configuration.UnleashTestSelectionKey:    true,
				configuration.UnleashSelectionConfirmKey: 5,
				configuration.UnleashWorkersKey:          tc.workers,
			}, nil)
			if !maps.Equal(with, tc.with) || !strings.Contains(heard, tc.heard) {
				t.Errorf("with selection: %v, want %v; logged %q, want %q", with, tc.with, heard, tc.heard)
			}
			if without, _ := unleash(t, dir, map[string]any{configuration.UnleashWorkersKey: tc.workers}, nil); !maps.Equal(without, tc.without) {
				t.Errorf("without selection: %v, want %v", without, tc.without)
			}
		})
	}
}

// verdictCases are fixtures of testdata whose runs fail for a reason the mutation may not have
// caused, the settings they run with, the files the source has beside it, and each verdict.
var verdictCases = []struct {
	fixture  string
	settings map[string]any
	beside   map[string]string
	want     map[string]mutator.Status
}{
	// The mutant does not build: a string cannot be subtracted.
	{"unbuilt", nil, nil, map[string]mutator.Status{"unbuilt.go:6 ARITHMETIC_BASE": mutator.NotViable}},
	// INVERT_LOGICAL builds, and vet refuses it; every test passes on it.
	{"vetlive", map[string]any{configuration.MutantTypeEnabledKey(mutator.InvertLogical): true}, nil,
		map[string]mutator.Status{"vetlive.go:6 CONDITIONALS_NEGATION": mutator.Lived, "vetlive.go:6 INVERT_LOGICAL": mutator.Lived}},
	// TestMain exits in every copy, on the unmutated code too: no verdict.
	{"outside", nil, map[string]string{"shared.txt": "shared"},
		map[string]mutator.Status{"outside.go:7 ARITHMETIC_BASE": mutator.Unclassified, "outside.go:10 ARITHMETIC_BASE": mutator.Unclassified}},
	// The replaced module is missing beside every copy: a setup failure, which is no verdict.
	{"relrep", nil, map[string]string{"ext/go.mod": "module example.com/ext\n\ngo 1.24\n", "ext/ext.go": "package ext\n\n// Zero is zero.\nconst Zero = 0\n"},
		map[string]mutator.Status{"relrep.go:8 ARITHMETIC_BASE": mutator.Unclassified, "relrep.go:11 ARITHMETIC_BASE": mutator.Unclassified}},
	// The killed mutant of a.go cannot be undone; b.go's mutant runs in a fresh copy.
	{"dirty", map[string]any{configuration.UnleashWorkersKey: 1}, nil,
		map[string]mutator.Status{"a.go:6 ARITHMETIC_BASE": mutator.Killed, "b.go:4 ARITHMETIC_BASE": mutator.Lived}},
}

func TestEachMutantsVerdictRestsOnItsOwnCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	for _, tc := range verdictCases {
		t.Run(tc.fixture, func(t *testing.T) {
			got, logged := unleash(t, filepath.Join("testdata", tc.fixture), tc.settings, tc.beside)
			if !maps.Equal(got, tc.want) {
				t.Fatalf("verdicts %v, want %v; logged %s", got, tc.want, logged)
			}
		})
	}
}
