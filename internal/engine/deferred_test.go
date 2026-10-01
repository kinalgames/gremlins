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
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/engine/workdir"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

const pkgP = "example.com/p"

var (
	arithmeticBase = mutator.ArithmeticBase.String()
	boundary       = mutator.ConditionalsBoundary.String()
)

func TestDeferNamesEachPendingMutantWithWhatSettleNeeds(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, hanging}}
	sel := selection.New(&indexStub{}, 0, (&heard{}).report)
	dealer, _ := aloneDealer(t, seq, "", engine.WithSelector(sel))
	muts := []*mutantStub{mutantAt(mutator.Runnable, 8), mutantAt(mutator.Runnable, 3), mutantAt(mutator.Runnable, 12)}
	var pending []mutator.Mutator
	for _, mut := range muts {
		start(dealer, mut, "w")
		pending = append(pending, mut)
	}
	sel.Disable(pkgP, errors.New("a disagreement"))
	got := dealer.Defer(pending)
	want := engine.Deferred{Timeout: aloneElapsed * engine.DefaultTimeoutCoefficient, Disabled: []string{pkgP}, Mutants: []engine.DeferredMutant{
		{File: fileA, Line: 3, Column: 2, Type: boundary, Pkg: pkgP, Kill: true, Confirm: true},
		{File: fileA, Line: 8, Column: 2, Type: boundary, Pkg: pkgP, Kill: true},
		{File: fileA, Line: 12, Column: 2, Type: boundary, Pkg: pkgP, First: "timed out"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deferred %+v\nwant %+v", got, want)
	}
}

// sumSource has an ARITHMETIC_BASE mutant at line 4, column 11 and another at line 8, column 11.
const sumSource = "package p\n\nfunc F(a, b int) int {\n\treturn a + b\n}\n\nfunc G(a, b int) int {\n\treturn a - b\n}\n"

// resumeDealer is a dealer running seq's commands on a module whose a.go holds sumSource, with sel
// as its selector when given.
func resumeDealer(t *testing.T, seq *sequence, sel *selection.Selector) *engine.MutantExecutorDealer {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileA), []byte(sumSource), 0o600); err != nil {
		t.Fatal(err)
	}
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
	d := engine.Deferred{Timeout: aloneElapsed * 3, Disabled: []string{"example.com/q"}, Mutants: []engine.DeferredMutant{
		{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true, Confirm: true},
		{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, First: "crashed"},
	}}
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
	d := engine.Deferred{Timeout: aloneElapsed * 3, Mutants: []engine.DeferredMutant{
		{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true},
		{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true},
	}}
	muts, err := resumeDealer(t, seq, nil).Resume(context.Background(), d)
	if err != nil || len(muts) != 2 || muts[0].Status() != mutator.Killed || muts[1].Status() != mutator.Killed || len(seq.all()) != 0 {
		t.Fatalf("%v after %v: %v", muts, seq.all(), err)
	}
}

func TestResumeWithoutASelectorStillConfirmsAKill(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure}}
	d := engine.Deferred{Timeout: aloneElapsed * 3, Mutants: []engine.DeferredMutant{
		{File: fileA, Line: 8, Column: 11, Type: arithmeticBase, Pkg: pkgP, Kill: true, Confirm: true},
	}}
	muts, err := resumeDealer(t, seq, nil).Resume(context.Background(), d)
	if err != nil || len(muts) != 1 || muts[0].Status() != mutator.Killed {
		t.Fatalf("%v: %v", muts, err)
	}
}

// resumeErrors are deferred mutants Resume cannot find or settle, and its error.
var resumeErrors = []struct {
	name string
	dm   engine.DeferredMutant
	want string
}{
	{"a file the source lacks", engine.DeferredMutant{File: "b.go", Line: 4, Column: 11, Type: arithmeticBase, Kill: true}, "b.go"},
	{"a position without that mutant", engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: "INVERT_LOGICAL", Kill: true}, "no INVERT_LOGICAL mutant at a.go:4:11"},
	{"another column", engine.DeferredMutant{File: fileA, Line: 4, Column: 12, Type: arithmeticBase, Kill: true}, "no ARITHMETIC_BASE mutant at a.go:4:12"},
	{"another line", engine.DeferredMutant{File: fileA, Line: 5, Column: 11, Type: arithmeticBase, Kill: true}, "no ARITHMETIC_BASE mutant at a.go:5:11"},
	{"neither a run to repeat nor a kill", engine.DeferredMutant{File: fileA, Line: 4, Column: 11, Type: arithmeticBase, First: "passed"}, "neither a run to repeat nor a kill"},
}

func TestResumeRefusesAMutantItCannotSettle(t *testing.T) {
	for _, tc := range resumeErrors {
		seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
		_, err := resumeDealer(t, seq, nil).Resume(context.Background(), engine.Deferred{Mutants: []engine.DeferredMutant{tc.dm}})
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(seq.all()) != 0 {
			t.Errorf("%s: %v after %v, want %q before any run", tc.name, err, seq.all(), tc.want)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileA), []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	dealer := engine.NewExecutorDealer(gomodule.GoModule{Name: pkgP, Root: root, CallingDir: "."}, tempDealer{newWdDealerStub(t)}, expectedTimeout)
	if _, err := dealer.Resume(context.Background(), engine.Deferred{Mutants: []engine.DeferredMutant{resumeErrors[2].dm}}); err == nil {
		t.Error("resumed in a file that does not parse")
	}
}

func TestADeferredRunIsReadAsItWasWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deferred.json")
	d := engine.Deferred{Timeout: 3, Disabled: []string{pkgP}, Mutants: []engine.DeferredMutant{{File: "sub/a.go", Line: 1, Column: 2, Type: arithmeticBase, Pkg: pkgP, First: "crashed"}}}
	if err := engine.WriteDeferred(path, d); err != nil {
		t.Fatal(err)
	}
	if got, err := engine.ReadDeferred(path); err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("%+v: %v", got, err)
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

// deferrerStub is a settlerStub that defers its pending mutants with no state.
type deferrerStub struct{ settlerStub }

func (*deferrerStub) Defer([]mutator.Mutator) engine.Deferred { return engine.Deferred{Timeout: 1} }

// deferCases are a dealer, where --settle-later points (relative to a temporary directory; ""
// for unset), and whether the run settles its pending mutants itself.
var deferCases = []struct {
	name    string
	dealer  func() settlerDealer
	path    string
	settles bool
}{
	{"a dealer that defers, to a file", func() settlerDealer { return &deferrerStub{} }, "deferred.json", false},
	{"a dealer that defers, to a file that cannot be written", func() settlerDealer { return &deferrerStub{} }, "missing/deferred.json", true},
	{"a dealer that defers, unset", func() settlerDealer { return &deferrerStub{} }, "", true},
	{"a dealer that cannot defer", func() settlerDealer { return &settlerStub{} }, "deferred.json", true},
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
			viperSet(map[string]any{configuration.UnleashSettleLaterKey: path})
			defer viperReset()
			mapFS, mod, c := loadFixture(defaultFixture, ".")
			defer c()
			d := tc.dealer()
			mut := engine.New(mod, testCodeData, d, engine.WithDirFs(mapFS))
			res := mut.Run(context.Background())
			_, err := os.Stat(path)
			if settled := d.settledCount() == 1; settled != tc.settles || (len(res.Mutants) > 0) != tc.settles || (err == nil) == tc.settles {
				t.Fatalf("settled %t, %d mutants reported, file %v; want settled %t", settled, len(res.Mutants), err, tc.settles)
			}
		})
	}
}
