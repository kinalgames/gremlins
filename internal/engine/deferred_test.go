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

package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/engine/workdir"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

const pkgP = "example.com/p"

// Strings the deferred-run tests share.
const (
	crashedRun    = "crashed"
	timesAB       = "a * b"
	plusAB        = "a + b"
	fileB         = "b.go"
	otherSettings = "had the settings"
	subDir        = "sub"
	deferredJSON  = "deferred.json"
	slowTag       = "slow"
	allPkgs       = "./..."
	fileP         = "p/a.go"
	otherContent  = "other"
)

var (
	arithmeticBase = mutator.ArithmeticBase.String()
	boundary       = mutator.ConditionalsBoundary.String()
	extraType      = mutator.Extra.String()
)

func TestDeferNamesEachPendingMutantWithWhatSettleNeeds(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, hanging}}
	sel := selection.New(&indexStub{}, 0, (&heard{}).report)
	dealer, _ := aloneDealerIn(t, sumRoot(t), seq, "", engine.WithSelector(sel))
	muts := []*mutantStub{mutantAt(mutator.Runnable, 8), mutantAt(mutator.Runnable, 3), mutantAt(mutator.Runnable, 12)}
	var pending []mutator.Mutator
	for _, mut := range muts {
		start(dealer, mut, "w")
		pending = append(pending, mut)
	}
	sel.Disable(pkgP, errors.New("a disagreement"))
	got, err := dealer.Defer(pending)
	want := engine.Deferred{
		Timeout: aloneElapsed * engine.DefaultTimeoutCoefficient, Settings: engine.Settings{Package: "."},
		Files: []engine.DeferredFile{{Path: fileA, SHA256: sumDigest}}, Disabled: []string{pkgP},
		Mutants: []engine.DeferredMutant{
			{File: fileA, Line: 3, Column: 2, Type: boundary, Pkg: pkgP, Kill: true, Confirm: true},
			{File: fileA, Line: 8, Column: 2, Type: boundary, Pkg: pkgP, Kill: true},
			{File: fileA, Line: 12, Column: 2, Type: boundary, Pkg: pkgP, First: "timed out"},
		},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred %+v: %v\nwant %+v", got, err, want)
	}
}

// deferSettings are runs' settings a Deferred records, each different from the default's.
var deferSettings = []struct {
	settings map[string]any
	want     engine.Settings
}{
	{map[string]any{configuration.UnleashTagsKey: slowTag}, engine.Settings{Package: ".", Tags: slowTag}},
	{map[string]any{configuration.UnleashIntegrationMode: true, configuration.UnleashTestCPUKey: 4}, engine.Settings{Package: ".", Integration: true, TestCPU: 1}},
	{map[string]any{configuration.UnleashTestCPUKey: 4}, engine.Settings{Package: ".", TestCPU: 4}},
	{map[string]any{configuration.UnleashCoverPkgKey: allPkgs}, engine.Settings{Package: ".", CoverPkg: allPkgs}},
}

func TestDeferRecordsTheSettingsThatChangeAVerdict(t *testing.T) {
	for _, tc := range deferSettings {
		t.Run(fmt.Sprint(tc.settings), func(t *testing.T) {
			viperSet(tc.settings)
			defer viperReset()
			dealer, _ := aloneDealerIn(t, sumRoot(t), &sequence{}, "")
			if got, err := dealer.Defer(nil); err != nil || got.Settings != tc.want {
				t.Errorf("%+v: %v, want %+v", got.Settings, err, tc.want)
			}
		})
	}
}

func TestDeferNamesAnExtraMutantByItsTextsAndEachFileOnce(t *testing.T) {
	root := sumRoot(t)
	mod := gomodule.GoModule{Name: pkgP, Root: root, CallingDir: "."}
	list := filepath.Join(t.TempDir(), "mutants.txt")
	if err := os.WriteFile(list, []byte("\na.go|a + b|a - b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extras, err := engine.ReadExtraMutants(list, mod)
	if err != nil {
		t.Fatal(err)
	}
	dealer, _ := aloneDealerIn(t, root, &sequence{}, "")
	got, err := dealer.Defer(append([]mutator.Mutator{mutantAt(mutator.Runnable, 8)}, extras...))
	want := []engine.DeferredMutant{
		{File: fileA, Line: 4, Column: 9, Type: extraType, Pkg: pkgP, Original: plusAB, Mutated: "a - b", ListLine: 2},
		{File: fileA, Line: 8, Column: 2, Type: boundary, Pkg: pkgP},
	}
	if err != nil || !reflect.DeepEqual(got.Mutants, want) || !reflect.DeepEqual(got.Files, []engine.DeferredFile{{Path: fileA, SHA256: sumDigest}}) {
		t.Fatalf("%+v, %+v: %v", got.Mutants, got.Files, err)
	}
}

func TestDeferFailsWhenItCannotReadAPendingMutantsPackage(t *testing.T) {
	dealer, _ := aloneDealer(t, &sequence{}, "")
	mut := mutantAt(mutator.Runnable, 8)
	mut.position.Filename = "missing/a.go"
	if _, err := dealer.Defer([]mutator.Mutator{mut}); err == nil {
		t.Fatal("deferred a mutant of a directory it could not read")
	}
}

// treeFiles are the files of a module whose package p has a pending mutant, each with whether a
// Deferred records it: the package's files and those of the directories below it, but for one
// whose name starts with "." or "_" and a nested module, and the module's go.mod and go.sum.
var treeFiles = []struct {
	path     string
	recorded bool
}{
	{"go.mod", true},
	{"go.sum", true},
	{fileP, true},
	{"p/a_test.go", true},
	{"p/testdata/in.txt", true},
	{"p/static/page.html", true},
	{"p/sub/b.go", true},
	{"p/.cache/c.txt", false},
	{"p/_build/d.go", false},
	{"p/nested/go.mod", false},
	{"p/nested/n.go", false},
	{"q/c.go", false},
	{"README.md", false},
}

// treeRoot is a module root holding treeFiles, p/a.go with sumSource as its content and every
// other file with its path.
func treeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range treeFiles {
		name := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		content := f.path
		if f.path == fileP {
			content = sumSource
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func TestDeferRecordsEveryFileAPendingMutantsVerdictCanRestOn(t *testing.T) {
	dealer, _ := aloneDealerIn(t, treeRoot(t), &sequence{}, "")
	mut := mutantAt(mutator.Runnable, 8)
	mut.position.Filename = fileP
	got, err := dealer.Defer([]mutator.Mutator{mut})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
	}
	var want []string
	for _, f := range treeFiles {
		if f.recorded {
			want = append(want, f.path)
		}
	}
	slices.Sort(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("recorded %v, want %v", paths, want)
	}
	// The hex SHA-256 of "p/a_test.go" (sha256sum).
	if got.Files[3].SHA256 != "9c52f4af99f845fe63e000aa63c947fd2872518fa95c1042e1cd8dce856b3d61" {
		t.Errorf("p/a_test.go recorded with %s", got.Files[3].SHA256)
	}
}

func TestDeferRecordsTheToolchain(t *testing.T) {
	tc := engine.Toolchain{GoVersion: "go1.27.1", GOOS: "linux", GOARCH: "arm64", GOFLAGS: "-mod=mod"}
	dealer, _ := aloneDealerIn(t, sumRoot(t), &sequence{}, "", engine.WithToolchain(tc))
	if got, err := dealer.Defer(nil); err != nil || got.Settings.Toolchain != tc {
		t.Errorf("%+v: %v, want %+v", got.Settings, err, tc)
	}
}

func TestDeferFailsOnAModuleFileItCannotRead(t *testing.T) {
	root := sumRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "go.sum", "inside"), 0o750); err != nil {
		t.Fatal(err)
	}
	dealer, _ := aloneDealerIn(t, root, &sequence{}, "")
	if _, err := dealer.Defer([]mutator.Mutator{mutantAt(mutator.Runnable, 8)}); err == nil {
		t.Fatal("deferred without the digest of go.sum")
	}
}

// treeChanges are changes to the module of treeRoot after a run deferred a mutant of its package
// p: the file written, its content, and the start of the error of the run that settles the
// mutant, "" for a change of a file the verdict cannot rest on, which the run settles in.
var treeChanges = []struct {
	path, content, want string
}{
	{"p/a_test.go", "package p", "p/a_test.go changed since"},
	{"p/testdata/in.txt", otherContent, "p/testdata/in.txt changed since"},
	{"p/static/page.html", otherContent, "p/static/page.html changed since"},
	{"p/sub/new.go", "package sub", "p/sub/new.go is new since"},
	{"go.mod", "module other", "go.mod changed since"},
	{"go.sum", "", "go.sum changed since"},
	{"q/c.go", "package q", ""},
	{"p/.cache/c.txt", otherContent, ""},
	{"p/_build/d.go", "package d", ""},
	{"p/nested/n.go", "package n", ""},
	{"README.md", otherContent, ""},
}

func TestResumeRefusesATreeThatChangedInAFileItRecorded(t *testing.T) {
	for _, tc := range treeChanges {
		t.Run(tc.path, func(t *testing.T) {
			root := treeRoot(t)
			deferring, _ := aloneDealerIn(t, root, &sequence{}, "")
			mut := mutantAt(mutator.Runnable, 8)
			mut.position.Filename = fileP
			d, err := deferring.Defer([]mutator.Mutator{mut})
			if err != nil {
				t.Fatal(err)
			}
			d.Mutants = []engine.DeferredMutant{{File: fileP, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, First: crashedRun}}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(tc.path)), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
			mod := gomodule.GoModule{Name: pkgP, Root: root, CallingDir: "."}
			settling := engine.NewExecutorDealer(mod, workdir.NewCachedDealer(t.TempDir(), root), expectedTimeout, engine.WithExecContext(seq.exec))
			_, err = settling.Resume(context.Background(), d)
			if tc.want == "" && (err != nil || len(seq.all()) != 1) {
				t.Errorf("%v after %v, want the mutant run alone", err, seq.all())
			}
			if tc.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.want) || len(seq.all()) != 0) {
				t.Errorf("%v after %v, want %q before any run", err, seq.all(), tc.want)
			}
		})
	}
}

// sumSource has an ARITHMETIC_BASE mutant at line 4, column 11 and another at line 8, column 11.
const sumSource = "package p\n\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G(a, b int) int {\n\treturn a - b\n}\n"

// sumDigest is the hex SHA-256 of sumSource (sha256sum).
const sumDigest = "dc3d814e6e887f12fd580f2f38b5fb4cdfc1f3f0d78699399aedfab669f43ad9"

// sumRoot is a module root whose a.go holds sumSource.
func sumRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileA), []byte(sumSource), 0o600); err != nil {
		t.Fatal(err)
	}

	return root
}

// deferredOf is a Deferred of the module of resumeDealer with the mutants dms.
func deferredOf(dms ...engine.DeferredMutant) engine.Deferred {
	return engine.Deferred{
		Timeout: aloneElapsed * 3, Settings: engine.Settings{Package: "."},
		Files: []engine.DeferredFile{{Path: fileA, SHA256: sumDigest}}, Mutants: dms,
	}
}

// resumeDealer is a dealer running seq's commands on a module whose a.go holds sumSource, with sel
// as its selector when given.
func resumeDealer(t *testing.T, seq *sequence, sel *selection.Selector) *engine.MutantExecutorDealer {
	t.Helper()
	root := sumRoot(t)
	mod := gomodule.GoModule{Name: pkgP, Root: root, CallingDir: "."}
	opts := []engine.ExecutorDealerOption{engine.WithExecContext(seq.exec)}
	if sel != nil {
		opts = append(opts, engine.WithSelector(sel))
	}

	return engine.NewExecutorDealer(mod, workdir.NewCachedDealer(t.TempDir(), root), expectedTimeout, opts...)
}

func TestResumeSettlesTheDeferredMutantsInTheirSource(t *testing.T) {
	// The kill's confirmation passes, the crash repeats alone and the unmutated code passes.
	seq := &sequence{runs: []execContext{fakeExecCommandSuccess, crashedBinary, fakeExecCommandSuccess}}
	sel := selection.New(&indexStub{}, 0, (&heard{}).report)
	d := deferredOf(
		engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true, Confirm: true},
		engine.DeferredMutant{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, First: crashedRun},
	)
	d.Disabled = []string{"example.com/q"}
	muts, err := resumeDealer(t, seq, sel).Resume(context.Background(), d)
	if err != nil || len(muts) != 2 {
		t.Fatalf("%v: %v", muts, err)
	}
	if muts[0].Status() != mutator.Lived || muts[1].Status() != mutator.Killed || len(seq.all()) != 3 {
		t.Errorf("%v, %v after %v", muts[0].Status(), muts[1].Status(), seq.all())
	}
	if muts[0].Position().Filename != fileA || muts[1].Position().Line != 8 || muts[1].Type() != mutator.ArithmeticBase {
		t.Errorf("found %v %v, %v %v", muts[0].Position(), muts[0].Type(), muts[1].Position(), muts[1].Type())
	}
	if !sel.Disabled(pkgP) || !sel.Disabled("example.com/q") {
		t.Errorf("disabled %t, %t", sel.Disabled(pkgP), sel.Disabled("example.com/q"))
	}
	for _, args := range seq.all() {
		if runArg(args) != "" || !strings.Contains(strings.Join(args, " "), "-timeout 5s") {
			t.Errorf("%v: every test, within the deferred time", args)
		}
	}
}

func TestResumeConfirmsOnlyTheKillsTheDeferringRunNamed(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
	d := deferredOf(
		engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true},
		engine.DeferredMutant{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true},
	)
	muts, err := resumeDealer(t, seq, nil).Resume(context.Background(), d)
	if err != nil || len(muts) != 2 || muts[0].Status() != mutator.Killed || muts[1].Status() != mutator.Killed || len(seq.all()) != 0 {
		t.Fatalf("%v after %v: %v", muts, seq.all(), err)
	}
}

func TestResumeWithoutASelectorStillConfirmsAKill(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure}}
	d := deferredOf(engine.DeferredMutant{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true, Confirm: true})
	muts, err := resumeDealer(t, seq, nil).Resume(context.Background(), d)
	if err != nil || len(muts) != 1 || muts[0].Status() != mutator.Killed {
		t.Fatalf("%v: %v", muts, err)
	}
}

func TestResumeSettlesAnExtraMutantInItsCopy(t *testing.T) {
	// The crash repeats alone and the unmutated code passes.
	seq := &sequence{runs: []execContext{crashedBinary, fakeExecCommandSuccess}}
	d := deferredOf(engine.DeferredMutant{File: fileA, Line: 4, Column: 9, Type: extraType, Pkg: pkgP, Original: plusAB, Mutated: timesAB, ListLine: 7, First: crashedRun})
	muts, err := resumeDealer(t, seq, nil).Resume(context.Background(), d)
	if err != nil || len(muts) != 1 || muts[0].Status() != mutator.Killed || muts[0].Type() != mutator.Extra || len(seq.all()) != 2 {
		t.Fatalf("%v after %v: %v", muts, seq.all(), err)
	}
	if tm, ok := muts[0].(*engine.TextMutator); !ok || tm.ListLine() != 7 {
		t.Errorf("%#v, want the list's line 7", muts[0])
	}
}

// resumeErrors are deferred mutants Resume cannot find or settle, and its error.
var resumeErrors = []struct {
	name string
	dm   engine.DeferredMutant
	want string
}{
	{"a file the run did not record", engine.DeferredMutant{File: fileB, Line: 4, Column: 11, Type: arithmeticBase, Kill: true}, "b.go is not among the files"},
	{"a position without that mutant", engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: "INVERT_LOGICAL", Kill: true}, "no INVERT_LOGICAL mutant at a.go:4:11"},
	{"another column", engine.DeferredMutant{File: fileA, Line: 4, Column: 12, Type: arithmeticBase, Kill: true}, "no ARITHMETIC_BASE mutant at a.go:4:12"},
	{"another line", engine.DeferredMutant{File: fileA, Line: 5, Column: 11, Type: arithmeticBase, Kill: true}, "no ARITHMETIC_BASE mutant at a.go:5:11"},
	{"neither a run to repeat nor a kill", engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, First: "passed"}, "neither a run to repeat nor a kill"},
	{"an extra mutant elsewhere", engine.DeferredMutant{File: fileA, Line: 5, Column: 9, Type: extraType, Original: plusAB, Mutated: timesAB, Kill: true}, "no EXTRA mutant at a.go:5:9"},
	{"an extra mutant whose text is not there", engine.DeferredMutant{File: fileA, Line: 4, Column: 9, Type: extraType, Original: "a / b", Mutated: timesAB, Kill: true}, "occurs 0 times"},
}

func TestResumeRefusesAMutantItCannotSettle(t *testing.T) {
	for _, tc := range resumeErrors {
		seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
		_, err := resumeDealer(t, seq, nil).Resume(context.Background(), deferredOf(tc.dm))
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(seq.all()) != 0 {
			t.Errorf("%s: %v after %v, want %q before any run", tc.name, err, seq.all(), tc.want)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileA), []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	dealer := engine.NewExecutorDealer(gomodule.GoModule{Name: pkgP, Root: root, CallingDir: "."}, tempDealer{newWdDealerStub(t)}, expectedTimeout)
	d := deferredOf(resumeErrors[2].dm)
	d.Files[0].SHA256 = packageDigest
	if _, err := dealer.Resume(context.Background(), d); err == nil || strings.Contains(err.Error(), "no ARITHMETIC_BASE") {
		t.Errorf("%v, want the parser's error", err)
	}
}

// packageDigest is the hex SHA-256 of "package" (sha256sum).
const packageDigest = "bc4a71180870f7945155fbb02f4b0a2e3faa2a62d6d31b7039013055ed19869a"

// sameRunErrors are deferred runs Resume refuses before it looks for any mutant: they changed
// from the deferred run, and its error.
var sameRunErrors = []struct {
	name   string
	change func(d *engine.Deferred)
	want   string
}{
	{"other build tags", func(d *engine.Deferred) { d.Settings.Tags = slowTag }, otherSettings},
	{"another package directory", func(d *engine.Deferred) { d.Settings.Package = subDir }, otherSettings},
	{"the integration mode", func(d *engine.Deferred) { d.Settings.Integration = true }, otherSettings},
	{"another -cpu", func(d *engine.Deferred) { d.Settings.TestCPU = 2 }, otherSettings},
	{"other coverage packages", func(d *engine.Deferred) { d.Settings.CoverPkg = allPkgs }, otherSettings},
	{"a changed file", func(d *engine.Deferred) { d.Files[0].SHA256 = packageDigest }, "a.go changed since the run that deferred its mutants"},
	{"a file that is gone", func(d *engine.Deferred) { d.Files = append(d.Files, engine.DeferredFile{Path: fileB}) }, "b.go is gone since the run that deferred its mutants"},
	{"no file recorded", func(d *engine.Deferred) { d.Files = nil }, "a.go is new since the run that deferred its mutants"},
	{"another toolchain", func(d *engine.Deferred) { d.Settings.Toolchain.GoVersion = "go1.26.4" }, otherSettings},
	{"other GOFLAGS", func(d *engine.Deferred) { d.Settings.Toolchain.GOFLAGS = "-race" }, otherSettings},
}

func TestResumeRefusesARunThatChanged(t *testing.T) {
	for _, tc := range sameRunErrors {
		seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
		d := deferredOf(engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, First: crashedRun})
		tc.change(&d)
		if _, err := resumeDealer(t, seq, nil).Resume(context.Background(), d); err == nil || !strings.Contains(err.Error(), tc.want) || len(seq.all()) != 0 {
			t.Errorf("%s: %v after %v, want %q before any run", tc.name, err, seq.all(), tc.want)
		}
	}
}

func TestADeferredRunIsReadAsItWasWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, deferredJSON)
	d := deferredOf(engine.DeferredMutant{File: "sub/a.go", Line: 1, Column: 2, Type: extraType, Pkg: pkgP, Original: "x", Mutated: "y", First: crashedRun})
	d.Disabled = []string{pkgP}
	if err := os.WriteFile(path, []byte("an earlier run's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.WriteDeferred(path, d); err != nil {
		t.Fatal(err)
	}
	if got, err := engine.ReadDeferred(path); err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("%+v: %v", got, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("%v beside the file: %v", entries, err)
	}
	if _, err := engine.ReadDeferred(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("read a file that does not exist")
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReadDeferred(path); err == nil {
		t.Error("read a file that is not JSON")
	}
}

func TestAWriteThatFailsLeavesNoFileBehind(t *testing.T) {
	dir := t.TempDir()
	taken := filepath.Join(dir, "taken")
	if err := os.MkdirAll(filepath.Join(taken, "inside"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := engine.WriteDeferred(taken, deferredOf()); err == nil {
		t.Fatal("wrote over a directory")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "taken" {
		t.Errorf("%v left: %v", entries, err)
	}
	if err := engine.WriteDeferred(filepath.Join(dir, "missing", deferredJSON), deferredOf()); err == nil {
		t.Error("wrote into a directory that does not exist")
	}
}

func TestRemoveDeferredLeavesNoFileAtThePath(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, deferredJSON)
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.RemoveDeferred(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file is still there: %v", err)
	}
	if err := engine.RemoveDeferred(stale); err != nil {
		t.Errorf("a path without a file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "full", "inside"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := engine.RemoveDeferred(filepath.Join(dir, "full")); err == nil {
		t.Error("removed a directory that holds a file")
	}
}

// deferrerStub is a settlerStub that defers its pending mutants with no state, or fails with err.
type deferrerStub struct {
	settlerStub
	err error
}

func (d *deferrerStub) Defer([]mutator.Mutator) (engine.Deferred, error) {
	return engine.Deferred{Timeout: 1}, d.err
}

// deferCases are a dealer, where --settle-later points (relative to a temporary directory; ""
// for unset), what an earlier run left there ("file", a "dir" holding a file, or nothing), and
// whether the run settles its pending mutants itself, which leaves no file there.
var deferCases = []struct {
	name    string
	dealer  func() settlerDealer
	path    string
	earlier string
	settles bool
}{
	{"a dealer that defers, to a file", func() settlerDealer { return &deferrerStub{} }, deferredJSON, "file", false},
	{"a dealer that defers, to a file that cannot be written", func() settlerDealer { return &deferrerStub{} }, "missing/deferred.json", "", true},
	{"a dealer that defers, unset", func() settlerDealer { return &deferrerStub{} }, "", "", true},
	{"a dealer that cannot defer", func() settlerDealer { return &settlerStub{} }, deferredJSON, "", true},
	{"a dealer that fails to defer", func() settlerDealer { return &deferrerStub{err: errors.New("no digest")} }, deferredJSON, "file", true},
	{"a dealer that fails to defer, to a path it cannot clear", func() settlerDealer { return &deferrerStub{err: errors.New("no digest")} }, deferredJSON, "dir", true},
}

// settlerDealer is what deferCases need of a dealer: it runs and settles mutants.
type settlerDealer interface {
	engine.ExecutorDealer
	engine.Settler
	settledCount() int
}

func (d *settlerStub) settledCount() int { return len(d.settled) }

func TestARunWithSettleLaterLeavesItsPendingMutantsToAFile(t *testing.T) {
	for _, tc := range deferCases {
		t.Run(tc.name, func(t *testing.T) {
			path := ""
			if tc.path != "" {
				path = filepath.Join(t.TempDir(), tc.path)
			}
			earlier(t, path, tc.earlier)
			viperSet(map[string]any{configuration.UnleashSettleLaterKey: path})
			defer viperReset()
			mapFS, mod, c := loadFixture(defaultFixture, ".")
			defer c()
			d := tc.dealer()
			mut := engine.New(mod, testCodeData, d, engine.WithDirFs(mapFS))
			res := mut.Run(context.Background())
			data, err := os.ReadFile(path) //nolint:gosec // the test's own temporary file
			if settled := d.settledCount() == 1; settled != tc.settles || (len(res.Mutants) > 0) != tc.settles || (err == nil) == tc.settles {
				t.Fatalf("settled %t, %d mutants reported, file %v; want settled %t", settled, len(res.Mutants), err, tc.settles)
			}
			if err == nil && res.Pending == 0 || err == nil && !strings.HasPrefix(string(data), `{"timeout":1,`) {
				t.Errorf("%d pending, the file holds %s", res.Pending, data)
			}
		})
	}
}

// earlier leaves at path what an earlier run left there: a "file", a "dir" holding a file, or
// nothing.
func earlier(t *testing.T, path, kind string) {
	t.Helper()
	if kind == "dir" {
		path = filepath.Join(path, "inside")
		if err := os.Mkdir(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if kind != "" {
		if err := os.WriteFile(path, []byte("an earlier run's"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResumeRefusesAnExtraMutantOutsideTheModule(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "m")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out.go"), []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	dealer, _ := aloneDealerIn(t, root, &sequence{}, "")
	d := engine.Deferred{
		Timeout: aloneElapsed, Settings: engine.Settings{Package: "."}, Files: []engine.DeferredFile{{Path: "../out.go", SHA256: packageDigest}},
		Mutants: []engine.DeferredMutant{{File: "../out.go", Line: 1, Column: 1, Type: extraType, Original: "package", Mutated: "x", Kill: true}},
	}
	if _, err := dealer.Resume(context.Background(), d); err == nil || !strings.Contains(err.Error(), "outside the module") {
		t.Fatalf("%v, want the file refused", err)
	}
}

// onePending is a deferrerStub whose first mutant alone is pending.
type onePending struct {
	deferrerStub
	asked int
}

func (d *onePending) Pending(mutator.Mutator) bool {
	d.asked++

	return d.asked == 1
}

func TestARunNamesHowManyMutantsWaitToRunAlone(t *testing.T) {
	out := &bytes.Buffer{}
	log.Reset()
	log.Init(out, &bytes.Buffer{})
	defer log.Reset()
	path := filepath.Join(t.TempDir(), deferredJSON)
	viperSet(map[string]any{configuration.UnleashSettleLaterKey: path})
	defer viperReset()
	mapFS, mod, c := loadFixture(defaultFixture, ".")
	defer c()
	e := engine.New(mod, testCodeData, &onePending{}, engine.WithDirFs(mapFS))
	res := e.Run(context.Background())
	if res.Pending != 1 || !strings.Contains(out.String(), "1 mutant waits to run alone: gremlins unleash --settle "+path) {
		t.Errorf("%d pending, logged %q", res.Pending, out.String())
	}
}
