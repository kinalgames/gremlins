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
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
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
// pkg is the package gremlins runs for, by its directory in the module ("" for the root).
func unleash(t *testing.T, dir, pkg string, settings map[string]any, beside map[string]string) (map[string]mutator.Status, string) {
	t.Helper()
	got, logged, err := unleashed(t, dir, pkg, settings, beside)
	if err != nil {
		t.Fatalf("%v; logged %s", err, logged)
	}

	return got, logged
}

// unleashed is unleash, answering the run's error rather than failing the test on it.
func unleashed(t *testing.T, dir, pkg string, settings map[string]any, beside map[string]string) (map[string]mutator.Status, string, error) {
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
	mod, err := gomodule.Init(filepath.Join(copied, filepath.FromSlash(pkg)))
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
	got := map[string]mutator.Status{}
	for _, m := range results.Mutants {
		got[fmt.Sprintf("%s:%d %s", filepath.Base(m.Position().Filename), m.Position().Line, m.Type())] = m.Status()
	}

	return got, errs.String(), err
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
			with, heard := unleash(t, dir, "", map[string]any{
				configuration.UnleashTestSelectionKey:    true,
				configuration.UnleashSelectionConfirmKey: 5,
				configuration.UnleashWorkersKey:          tc.workers,
			}, nil)
			if !maps.Equal(with, tc.with) || !strings.Contains(heard, tc.heard) {
				t.Errorf("with selection: %v, want %v; logged %q, want %q", with, tc.with, heard, tc.heard)
			}
			if without, _ := unleash(t, dir, "", map[string]any{configuration.UnleashWorkersKey: tc.workers}, nil); !maps.Equal(without, tc.without) {
				t.Errorf("without selection: %v, want %v", without, tc.without)
			}
		})
	}
}

// verdictCases are fixtures of testdata whose runs fail for a reason the mutation may not have
// caused, the settings they run with, the files the source has beside it, and each verdict.
var verdictCases = []struct {
	fixture  string
	pkg      string
	settings map[string]any
	beside   map[string]string
	want     map[string]mutator.Status
}{
	// The mutant does not build: a string cannot be subtracted.
	{"unbuilt", "", nil, nil, map[string]mutator.Status{"unbuilt.go:6 ARITHMETIC_BASE": mutator.NotViable}},
	// INVERT_LOGICAL builds, and vet refuses it; every test passes on it.
	{"vetlive", "", map[string]any{configuration.MutantTypeEnabledKey(mutator.InvertLogical): true}, nil,
		map[string]mutator.Status{"vetlive.go:6 CONDITIONALS_NEGATION": mutator.Lived, "vetlive.go:6 INVERT_LOGICAL": mutator.Lived}},
	// TestMain exits in every copy, on the unmutated code too: no verdict.
	{"outside", "", nil, map[string]string{"shared.txt": "shared"},
		map[string]mutator.Status{"outside.go:7 ARITHMETIC_BASE": mutator.Unclassified, "outside.go:10 ARITHMETIC_BASE": mutator.Unclassified}},
	// The replaced module is missing beside every copy: a setup failure, which is no verdict.
	{"relrep", "", nil, map[string]string{"ext/go.mod": "module example.com/ext\n\ngo 1.24\n", "ext/ext.go": "package ext\n\n// Zero is zero.\nconst Zero = 0\n"},
		map[string]mutator.Status{"relrep.go:8 ARITHMETIC_BASE": mutator.Unclassified, "relrep.go:11 ARITHMETIC_BASE": mutator.Unclassified}},
	// Each mutant panics in init: a crash that repeats alone, where the unmutated code passes.
	{"crashinit", "", ample, nil, map[string]mutator.Status{"crashinit.go:6 ARITHMETIC_BASE": mutator.Killed, "crashinit.go:6 CONDITIONALS_NEGATION": mutator.Killed}},
	// The killed mutant of a.go cannot be undone; b.go's mutant runs in a fresh copy.
	{"dirty", "", map[string]any{configuration.UnleashWorkersKey: 1}, nil,
		map[string]mutator.Status{"a.go:6 ARITHMETIC_BASE": mutator.Killed, "b.go:4 ARITHMETIC_BASE": mutator.Lived}},
	// The compiler names the line where each mutated operator's left operand starts.
	{"chain", "", nil, nil, map[string]mutator.Status{"chain.go:7 ARITHMETIC_BASE": mutator.NotViable, "chain.go:8 ARITHMETIC_BASE": mutator.NotViable}},
	// The same, and a one-line mutant, in a package below the module's root.
	{"subunbuilt", "services/x", map[string]any{configuration.UnleashExcludeFiles: []string{"/"}}, nil,
		map[string]mutator.Status{"x.go:5 ARITHMETIC_BASE": mutator.NotViable, "x.go:9 ARITHMETIC_BASE": mutator.NotViable, "x.go:10 ARITHMETIC_BASE": mutator.NotViable}},
	// The mutants of testdata/extra.txt, beside Gremlins' own: a comment's lives, the constant's
	// crashes alone again, the case's fails a test, and a statement that does not parse is no
	// verdict either way.
	{"extra", "", map[string]any{configuration.UnleashExtraMutantsKey: extraList, configuration.UnleashTimeoutCoefficientKey: 30}, nil,
		map[string]mutator.Status{
			"extra.go:5 EXTRA": mutator.Lived, "extra.go:6 EXTRA": mutator.Killed, "extra.go:11 EXTRA": mutator.Killed, "extra.go:15 EXTRA": mutator.NotViable,
			"extra.go:11 CONDITIONALS_BOUNDARY": mutator.NotCovered, "extra.go:11 CONDITIONALS_NEGATION": mutator.NotCovered,
		}},
}

// ample gives each mutant 30 times the unmutated run's time, for fixtures whose mutants crash: a
// crash ends at once, and a build of the mutant on a loaded machine can outlast the default 3
// times, which makes the run alone a time-out, so no repeat, and the mutant UNCLASSIFIED.
var ample = map[string]any{configuration.UnleashTimeoutCoefficientKey: 30}

// extraList is the list of the extra fixture's extra mutants, by its absolute path: the runs
// change the working directory.
var extraList, _ = filepath.Abs(filepath.Join("testdata", "extra.txt"))

func TestEachMutantsVerdictRestsOnItsOwnCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	for _, tc := range verdictCases {
		t.Run(tc.fixture, func(t *testing.T) {
			got, logged := unleash(t, filepath.Join("testdata", tc.fixture), tc.pkg, tc.settings, tc.beside)
			if !maps.Equal(got, tc.want) {
				t.Fatalf("verdicts %v, want %v; logged %s", got, tc.want, logged)
			}
		})
	}
}

// deferredCases are fixtures of verdictCases with mutants that run again alone.
var deferredCases = []string{"crashinit", "outside", "extra"}

func TestARunThatDefersItsRunsAloneLeavesTheVerdictsToTheRunThatSettlesThem(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	for _, tc := range verdictCases {
		if !slices.Contains(deferredCases, tc.fixture) {
			continue
		}
		t.Run(tc.fixture, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "deferred.json")
			settings := maps.Clone(tc.settings)
			if settings == nil {
				settings = map[string]any{}
			}
			settings[configuration.UnleashSettleLaterKey] = path
			first, logged := unleash(t, filepath.Join("testdata", tc.fixture), tc.pkg, settings, tc.beside)
			delete(settings, configuration.UnleashSettleLaterKey)
			settings[configuration.UnleashSettleKey] = path
			settled, settleLogged := unleash(t, filepath.Join("testdata", tc.fixture), tc.pkg, settings, tc.beside)
			all := maps.Clone(first)
			maps.Copy(all, settled)
			if len(settled) == 0 || len(all) != len(first)+len(settled) || !maps.Equal(all, tc.want) {
				t.Fatalf("deferring run %v, settling run %v, want %v, each mutant once; logged %s, then %s", first, settled, tc.want, logged, settleLogged)
			}
		})
	}
}

func TestASettlingRunRefusesAFileItCannotUse(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.json")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := engine.WriteDeferred(elsewhere, engine.Deferred{Mutants: []engine.DeferredMutant{{File: "unbuilt.go", Line: 1, Column: 1, Type: "ARITHMETIC_BASE", Kill: true}}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{missing, elsewhere} {
		configuration.Set(configuration.UnleashSettleKey, path)
		mod, err := gomodule.Init(filepath.Join("testdata", "unbuilt"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := run(context.Background(), mod, t.TempDir()); err == nil {
			t.Errorf("settled from %s", path)
		}
		configuration.Reset()
	}
}

// swapped is cmd/testdata/swap/swap.go with A's and B's lines swapped: each mutant's position
// now holds the other's.
const swapped = "// Package swap has two mutants of one type at one column: A's crashes once and no test checks\n" +
	"// it, B's a test kills.\npackage swap\n\n// A is n plus one.\nfunc B(n int) int { return n + 2 }\n\n" +
	"// B is n plus two.\nfunc A(n int) int { return n + 1 }\n"

func TestASettlingRunRefusesATreeThatChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("SWAP_MARKER", filepath.Join(t.TempDir(), "crashed"))
	dir := filepath.Join("testdata", "swap")
	path := filepath.Join(t.TempDir(), "deferred.json")
	if err := os.WriteFile(path, []byte("an earlier run's"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, logged := unleash(t, dir, "", map[string]any{configuration.UnleashSettleLaterKey: path, configuration.UnleashTimeoutCoefficientKey: 30}, nil)
	if !maps.Equal(first, map[string]mutator.Status{"swap.go:9 ARITHMETIC_BASE": mutator.Killed}) {
		t.Fatalf("the deferring run reported %v; logged %s", first, logged)
	}
	settings := map[string]any{configuration.UnleashSettleKey: path}
	if got, logged, err := unleashed(t, dir, "", settings, map[string]string{"swap/swap.go": swapped}); err == nil || !strings.Contains(err.Error(), "swap.go changed since the run that deferred its mutants") {
		t.Fatalf("settled %v in a changed tree: %v; logged %s", got, err, logged)
	}
	if got, logged, err := unleashed(t, dir, "", map[string]any{configuration.UnleashSettleKey: path, configuration.UnleashTagsKey: "slow"}, nil); err == nil || !strings.Contains(err.Error(), "had the settings") {
		t.Fatalf("settled %v with other tags: %v; logged %s", got, err, logged)
	}
	if got, logged := unleash(t, dir, "", settings, nil); !maps.Equal(got, map[string]mutator.Status{"swap.go:6 ARITHMETIC_BASE": mutator.Lived}) {
		t.Fatalf("settled %v in the tree that deferred it; logged %s", got, logged)
	}
}

func TestARunWithSettleLaterLeavesNoFileOfAnEarlierRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	path := filepath.Join(t.TempDir(), "deferred.json")
	if err := os.WriteFile(path, []byte("an earlier run's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := unleash(t, filepath.Join("testdata", "unbuilt"), "", map[string]any{configuration.UnleashSettleLaterKey: path}, nil); len(got) != 1 {
		t.Fatalf("verdicts %v", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a run without pending mutants left a file: %v", err)
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "inside"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unleashed(t, filepath.Join("testdata", "unbuilt"), "", map[string]any{configuration.UnleashSettleLaterKey: path}, nil); err == nil {
		t.Error("ran with a path it cannot clear")
	}
}

func TestARunRefusesAnExtraMutantItCannotApply(t *testing.T) {
	list := filepath.Join(t.TempDir(), "extra.txt")
	if err := os.WriteFile(list, []byte("extra.go|return|return true ||\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer configuration.Reset()
	configuration.Set(configuration.UnleashExtraMutantsKey, list)
	mod, err := gomodule.Init(filepath.Join("testdata", "extra"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), mod, t.TempDir()); err == nil || !strings.Contains(err.Error(), `"return" occurs 2 times, not once`) {
		t.Errorf("%v, want the extra mutant refused", err)
	}
}
