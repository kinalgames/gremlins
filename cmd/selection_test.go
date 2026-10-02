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

	return unleashIn(t, copied, pkg, settings)
}

// unleashIn is unleashed on the module in the directory copied, which it leaves as the run leaves
// it.
func unleashIn(t *testing.T, copied, pkg string, settings map[string]any) (map[string]mutator.Status, string, error) {
	t.Helper()
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
	t.Setenv("GOFLAGS", modMod)
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

// modMod is the GOFLAGS of the fixtures' runs: their modules have no go.sum.
const modMod = "-mod=mod"

// testedit is the fixture cmd/testdata/testedit's directory name.
const testedit = "testedit"

// extraList is the list of the extra fixture's extra mutants, by its absolute path: the runs
// change the working directory.
var extraList, _ = filepath.Abs(filepath.Join("testdata", "extra.txt"))

func TestEachMutantsVerdictRestsOnItsOwnCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
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
	t.Setenv("GOFLAGS", modMod)
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
	t.Setenv("GOFLAGS", modMod)
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

// testedited is cmd/testdata/testedit/testedit_test.go with a test that kills A's mutant.
const testedited = "package testedit\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n" +
	"func TestMain(m *testing.M) {\n\tif marker := os.Getenv(\"TESTEDIT_MARKER\"); marker != \"\" && A(1) != 2 {\n" +
	"\t\tif _, err := os.Stat(marker); err != nil {\n\t\t\t_ = os.WriteFile(marker, nil, 0o600)\n\t\t\tos.Exit(3)\n\t\t}\n\t}\n" +
	"\tos.Exit(m.Run())\n}\n\nfunc TestA(t *testing.T) {\n\tif A(1) != 2 {\n\t\tt.Fatal(\"A(1) must be 2\")\n\t}\n}\n"

// testeditRefusals are changes to cmd/testdata/testedit after a run deferred A's mutant (a file
// written beside the deferring run's tree, GOFLAGS), each with the start of the settling run's
// refusal.
var testeditRefusals = []struct {
	name    string
	beside  map[string]string
	goflags string
	want    string
}{
	{"a test that kills the mutant", map[string]string{"testedit/testedit_test.go": testedited}, modMod, "testedit_test.go changed since"},
	{"a new testdata file", map[string]string{"testedit/testdata/in.txt": "x"}, modMod, "testdata/in.txt is new since"},
	{"go.mod", map[string]string{"testedit/go.mod": "module example.com/testedit\n\ngo 1.25\n"}, modMod, "go.mod changed since"},
	{"other GOFLAGS", nil, "-mod=mod -count=1", "the run that deferred the mutants had the settings"},
}

func TestASettlingRunRefusesAnEditOfTheTestsOrTheToolchain(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
	t.Setenv("TESTEDIT_MARKER", filepath.Join(t.TempDir(), "crashed"))
	dir := filepath.Join("testdata", testedit)
	path := filepath.Join(t.TempDir(), "deferred.json")
	if first, logged := unleash(t, dir, "", map[string]any{configuration.UnleashSettleLaterKey: path, configuration.UnleashTimeoutCoefficientKey: 30}, nil); len(first) != 0 {
		t.Fatalf("the deferring run reported %v; logged %s", first, logged)
	}
	settings := map[string]any{configuration.UnleashSettleKey: path}
	for _, tc := range testeditRefusals {
		t.Setenv("GOFLAGS", tc.goflags)
		if got, logged, err := unleashed(t, dir, "", settings, tc.beside); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: settled %v: %v; logged %s", tc.name, got, err, logged)
		}
	}
	t.Setenv("GOFLAGS", modMod)
	if got, logged := unleash(t, dir, "", settings, nil); !maps.Equal(got, map[string]mutator.Status{"testedit.go:6 ARITHMETIC_BASE": mutator.Lived}) {
		t.Fatalf("settled %v in the tree that deferred it; logged %s", got, logged)
	}
}

// command runs the unleash command from the directory wd on the package pkg with settings and the
// arithmetic and conditional mutants, and answers what it logged and its error.
func command(t *testing.T, wd, pkg string, settings map[string]any) (string, error) {
	t.Helper()
	t.Chdir(wd)
	out := &bytes.Buffer{}
	log.Reset()
	log.Init(out, out)
	defer log.Reset()
	defer configuration.Reset()
	for k, v := range settings {
		configuration.Set(k, v)
	}
	for _, mt := range []mutator.Type{mutator.ArithmeticBase, mutator.ConditionalsBoundary, mutator.ConditionalsNegation} {
		configuration.Set(configuration.MutantTypeEnabledKey(mt), true)
	}
	err := runUnleash(context.Background())(nil, []string{pkg})

	return out.String(), err
}

// ownFilesCases are where a run starts, relative to the parent of a copy of the testedit module,
// and the package and the files its runs write, relative to where it starts: all of them inside
// the module, as the module's own directory (".") or a package named from above it leaves them.
var ownFilesCases = []struct {
	name, wd, pkg, dir string
}{
	{"from the module's directory", testedit, ".", ""},
	{"from above the module", ".", testedit, testedit},
}

// TestASettlingRunLeavesOutTheFilesTheRunsWrite runs the command with relative paths whose files
// all land in the module: the settle file, a temporary file WriteDeferred left beside it, and the
// reports (-o) of each run; the hint's command settles, and settles again over its own report.
func TestASettlingRunLeavesOutTheFilesTheRunsWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
	for _, tc := range ownFilesCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TESTEDIT_MARKER", filepath.Join(t.TempDir(), "crashed"))
			parent := t.TempDir()
			copied := filepath.Join(parent, testedit)
			if err := os.CopyFS(copied, os.DirFS(filepath.Join("testdata", testedit))); err != nil {
				t.Fatal(err)
			}
			wd := filepath.Join(parent, tc.wd)
			pending, report := filepath.Join(tc.dir, "pending.json"), filepath.Join(tc.dir, "report.json")
			deferring := map[string]any{configuration.UnleashSettleLaterKey: pending, configuration.UnleashOutputKey: report, configuration.UnleashTimeoutCoefficientKey: 30}
			logged, err := command(t, wd, tc.pkg, deferring)
			hint := "gremlins unleash --settle " + filepath.Join(copied, "pending.json") + " " + copied + "\n"
			if err != nil || !strings.Contains(logged, hint) {
				t.Fatalf("%v; logged %s, want the hint %q", err, logged, hint)
			}
			if err := os.WriteFile(filepath.Join(copied, "pending.json.123456"), []byte("a write that died"), 0o600); err != nil {
				t.Fatal(err)
			}
			settling := map[string]any{configuration.UnleashSettleKey: pending, configuration.UnleashOutputKey: filepath.Join(tc.dir, "settled.json")}
			for range 2 { // the second finds the first's report
				logged, err := command(t, wd, tc.pkg, settling)
				if err != nil || !strings.Contains(logged, "LIVED ARITHMETIC_BASE at testedit.go:6:30") {
					t.Fatalf("%v; logged %s", err, logged)
				}
			}
			if _, err := os.Stat(filepath.Join(copied, "settled.json")); err != nil {
				t.Errorf("the settling run's report: %v", err)
			}
		})
	}
}

// TestASettlingRunChecksAFileNamedAfterTheSettleFile defers a mutant of the escape fixture to the
// settle file check, beside check.go, and edits check.go so that TestA would kill the mutant.
func TestASettlingRunChecksAFileNamedAfterTheSettleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
	t.Setenv("ESCAPE_MARKER", filepath.Join(t.TempDir(), "crashed"))
	copied := filepath.Join(t.TempDir(), "escape")
	if err := os.CopyFS(copied, os.DirFS(filepath.Join("testdata", "escape"))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copied, "check")
	if first, logged, err := unleashIn(t, copied, "", map[string]any{configuration.UnleashSettleLaterKey: path, configuration.UnleashTimeoutCoefficientKey: 30}); err != nil || len(first) != 0 {
		t.Fatalf("the deferring run reported %v: %v; logged %s", first, err, logged)
	}
	strict := "package escape\n\n// Check reports whether v is the value A gives 1.\nfunc Check(v int) bool { return v == 2 }\n"
	if err := os.WriteFile(filepath.Join(copied, "check.go"), []byte(strict), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, logged, err := unleashIn(t, copied, "", map[string]any{configuration.UnleashSettleKey: path}); err == nil || !strings.Contains(err.Error(), "check.go changed since the run that deferred its mutants") {
		t.Fatalf("settled %v: %v; logged %s", got, err, logged)
	}
}

// settleJSON is a settle file as WriteDeferred writes one.
const settleJSON = `{"timeout":1,"settings":{"package":".","toolchain":{"go_version":"go1.27","goos":"linux","goarch":"amd64"}},"files":[],"mutants":[]}`

// The paths and refusals of foreignCases: a directory, a settle file's name, and the start of each
// setting's refusal.
const (
	testdataDir   = "testdata"
	pendingJSON   = "pending.json"
	refusedOutput = "--output "
	refusedLater  = "--settle-later "
	laterKey      = configuration.UnleashSettleLaterKey
	outputKey     = configuration.UnleashOutputKey
)

// foreignCases are files at the path a run with --settle-later names, relative to the escape
// fixture's copy: --settle-later itself, or --output beside a --settle-later path outside the
// package; the file's content ("" for none, but for the source check.go and the directory
// testdata), and the start of the run's refusal, "" for none.
var foreignCases = []struct {
	name, flag, path, content, want string
}{
	{"a source as the report", outputKey, "check.go", "", refusedOutput},
	{"a fixture in a report's shape as the report", outputKey, "testdata/report.json", `{"go_module":"example.com/stored","files":[]}`, refusedOutput},
	{"a directory as the report", outputKey, testdataDir, "", refusedOutput},
	{"no file as the report", outputKey, "none.json", "", ""},
	{"a fixture as the settle file", laterKey, "testdata/golden.json", `{"a": 1}`, refusedLater},
	{"a fixture in a settle file's shape", laterKey, "testdata/state.json", `{"settings":{"level":1},"mutants":["a"]}`, refusedLater},
	{"settings without mutants", laterKey, pendingJSON, `{"settings":{}}`, refusedLater},
	{"a settle file with a member it does not have", laterKey, pendingJSON, settleJSON[:len(settleJSON)-1] + `,"level":1}`, refusedLater},
	{"a settle file followed by more", laterKey, pendingJSON, settleJSON + "{}", refusedLater},
	{"a directory as the settle file", laterKey, testdataDir, "", refusedLater},
	{"a settle file", laterKey, pendingJSON, settleJSON, ""},
	{"no file as the settle file", laterKey, "none.json", "", ""},
}

func TestARunWithSettleLaterRefusesToNameAFileOfThePackage(t *testing.T) {
	for _, tc := range foreignCases {
		t.Run(tc.name, func(t *testing.T) {
			copied := filepath.Join(t.TempDir(), "escape")
			if err := os.CopyFS(copied, os.DirFS(filepath.Join("testdata", "escape"))); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(copied, filepath.FromSlash(tc.path))
			if tc.content != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.path == testdataDir {
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path) //nolint:gosec // the test's own copy
			defer configuration.Reset()
			configuration.Set(laterKey, filepath.Join(t.TempDir(), pendingJSON))
			configuration.Set(tc.flag, path)
			mod, err := gomodule.Init(copied)
			if err != nil {
				t.Fatal(err)
			}
			err = refuseForeign(mod)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.want+path)) {
				t.Errorf("%v, want %q", err, tc.want)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) { //nolint:gosec // the test's own copy
				t.Errorf("the file changed to %q", after)
			}
		})
	}
}

func TestARunWithoutSettleLaterChecksNoPath(t *testing.T) {
	defer configuration.Reset()
	copied := filepath.Join("testdata", "escape")
	configuration.Set(configuration.UnleashOutputKey, filepath.Join(copied, "check.go"))
	mod, err := gomodule.Init(copied)
	if err != nil {
		t.Fatal(err)
	}
	if err := refuseForeign(mod); err != nil {
		t.Errorf("%v, want no check: the run writes no settle file", err)
	}
}

func TestARunWithSettleLaterNamingAFileOfThePackageAsItsReportLeavesItAsItIs(t *testing.T) {
	for name, content := range map[string]string{"check.go": "", "testdata/report.json": `{"go_module":"example.com/stored","files":[]}`} {
		copied := filepath.Join(t.TempDir(), "escape")
		if err := os.CopyFS(copied, os.DirFS(filepath.Join("testdata", "escape"))); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(copied, filepath.FromSlash(name))
		if content != "" {
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(file) //nolint:gosec // the test's own copy
		if err != nil {
			t.Fatal(err)
		}
		pending := filepath.Join(t.TempDir(), "pending.json")
		settings := map[string]any{configuration.UnleashOutputKey: file, configuration.UnleashSettleLaterKey: pending}
		if _, logged, err := unleashIn(t, copied, "", settings); err == nil || !strings.Contains(err.Error(), "names an existing file of the package") {
			t.Fatalf("%s: %v; logged %s", name, err, logged)
		}
		if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, before) { //nolint:gosec // the test's own copy
			t.Errorf("%s is %q: %v", name, after, err)
		}
		if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: the refused run wrote its settle file: %v", name, err)
		}
	}
}

func TestRelativePathsAreReadFromWhereGremlinsStarted(t *testing.T) {
	defer configuration.Reset()
	wd := t.TempDir()
	abs := filepath.Join(t.TempDir(), "report.json")
	configuration.Set(configuration.UnleashSettleLaterKey, "pending.json")
	configuration.Set(configuration.UnleashOutputKey, abs)
	absolute(wd, configuration.UnleashSettleKey, configuration.UnleashSettleLaterKey, configuration.UnleashOutputKey)
	if got := configuration.Get[string](configuration.UnleashSettleLaterKey); got != filepath.Join(wd, "pending.json") {
		t.Errorf("settle-later %s", got)
	}
	if got := configuration.Get[string](configuration.UnleashOutputKey); got != abs {
		t.Errorf("output %s, want it as given", got)
	}
	if got := configuration.Get[string](configuration.UnleashSettleKey); got != "" {
		t.Errorf("settle %q, want it unset", got)
	}
}

func TestARunFailsWithoutTheToolchainASettlementNeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deferred.json")
	if err := engine.WriteDeferred(path, engine.Deferred{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, key := range []string{configuration.UnleashSettleKey, configuration.UnleashSettleLaterKey} {
		if _, _, err := unleashed(t, filepath.Join("testdata", "unbuilt"), "", map[string]any{key: path}, nil); err == nil || !strings.Contains(err.Error(), "failed to read the toolchain") {
			t.Errorf("%s: %v, want the go command's failure", key, err)
		}
	}
}

func TestARunWithSettleLaterLeavesNoFileOfAnEarlierRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
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
