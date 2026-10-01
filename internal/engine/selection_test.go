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
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/engine/workdir"
	"github.com/go-gremlins/gremlins/internal/engine/workerpool"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

const (
	// failTogether is in the report of a subset that fails on the unmutated code.
	failTogether = "fail together"
	fileA        = "a.go"
	testOne      = "^(TestOne)$"
	initmod      = "initmod"
)

// indexStub answers, for every package after delay, an index of a.go where TestOne covers lines
// 3 to 5, TestTwo lines 4 to 9 and TestThree lines 20 to 30; the patterns in failing fail on the
// unmutated code.
type indexStub struct {
	m       sync.Mutex
	calls   int
	delay   time.Duration
	failing map[string]bool
}

func (s *indexStub) Index(string) (selection.Index, error) {
	time.Sleep(s.delay)
	s.m.Lock()
	defer s.m.Unlock()
	s.calls++

	return selection.Index{
		Tests: []string{"TestOne", "TestTwo", "TestThree"},
		Profiles: []coverage.Profile{
			{fileA: {{StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 2}}},
			{fileA: {{StartLine: 4, StartCol: 1, EndLine: 9, EndCol: 2}}},
			{fileA: {{StartLine: 20, StartCol: 1, EndLine: 30, EndCol: 2}}},
		},
	}, nil
}

func (s *indexStub) Passes(_ selection.Index, pattern string) error {
	if s.failing[pattern] {
		return errors.New("FAIL")
	}

	return nil
}

// commands records every command a dealer starts, with the time left to it, and runs a command
// with -run as subset does and any other as whole does.
type commands struct {
	m             sync.Mutex
	runs          [][]string
	timeouts      []time.Duration
	subset, whole execContext
}

func (c *commands) exec(ctx context.Context, command string, args ...string) *exec.Cmd {
	deadline, _ := ctx.Deadline()
	c.m.Lock()
	defer c.m.Unlock()
	c.runs = append(c.runs, append([]string{command}, args...))
	c.timeouts = append(c.timeouts, time.Until(deadline))
	if slices.Contains(args, "-run") {
		return c.subset(ctx, command, args...)
	}

	return c.whole(ctx, command, args...)
}

func (c *commands) all() [][]string {
	c.m.Lock()
	defer c.m.Unlock()

	return slices.Clone(c.runs)
}

// runMutant runs mut, whose package's tests last elapsed, with a dealer starting got's commands,
// and answers the commands it started.
func runMutant(t *testing.T, mut *mutantStub, got *commands, elapsed time.Duration, opts ...engine.ExecutorDealerOption) [][]string {
	t.Helper()
	before := len(got.all())
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}
	dealer := engine.NewExecutorDealer(mod, tempDealer{newWdDealerStub(t)}, elapsed,
		append([]engine.ExecutorDealerOption{engine.WithExecContext(got.exec)}, opts...)...)
	start(dealer, mut, "selection")

	return got.all()[before:]
}

// newDealer is a dealer starting its commands with exec, its package's tests lasting
// expectedTimeout.
func newDealer(t *testing.T, exec execContext, opts ...engine.ExecutorDealerOption) *engine.MutantExecutorDealer {
	t.Helper()
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}

	return engine.NewExecutorDealer(mod, tempDealer{newWdDealerStub(t)}, expectedTimeout,
		append([]engine.ExecutorDealerOption{engine.WithExecContext(exec)}, opts...)...)
}

// start runs mut to its end on an executor of dealer.
func start(dealer engine.ExecutorDealer, mut mutator.Mutator, worker string) {
	outCh := make(chan mutator.Mutator)
	wg := sync.WaitGroup{}
	wg.Add(1)
	executor := dealer.NewExecutor(mut, outCh, &wg)
	go func() {
		<-outCh
		close(outCh)
	}()
	executor.Start(&workerpool.Worker{Name: worker, ID: 1})
	wg.Wait()
}

// tempDealer is a work directory dealer whose work directory exists.
type tempDealer struct{ *dealerStub }

func (d tempDealer) WorkDir() string { return d.t.TempDir() }

func mutantAt(status mutator.Status, line int) *mutantStub {
	return &mutantStub{status: status, mutType: mutator.ConditionalsBoundary, pkg: "example.com/p",
		position: token.Position{Filename: fileA, Line: line, Column: 2}}
}

// passing runs every command as a success.
func passing() *commands {
	return &commands{subset: fakeExecCommandSuccess, whole: fakeExecCommandSuccess}
}

// runArg is the -run pattern of a command, or "" for none.
func runArg(args []string) string {
	if i := slices.Index(args, "-run"); i >= 0 {
		return args[i+1]
	}

	return ""
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

func (h *heard) all() string {
	h.m.Lock()
	defer h.m.Unlock()

	return strings.Join(h.errs, "\n")
}

func TestAMutantRunsTheTestsThatCoverIt(t *testing.T) {
	cases := []struct {
		line int
		runs []string // the -run of each command: the subset, then every test to confirm it lives
	}{
		{3, []string{testOne, ""}},
		{4, []string{"^(TestOne|TestTwo)$", ""}},
		{12, []string{""}},
	}
	for _, tc := range cases {
		got := passing()
		mut := mutantAt(mutator.Runnable, tc.line)
		runs := runMutant(t, mut, got, expectedTimeout, engine.WithSelector(selection.New(&indexStub{}, 0, (&heard{}).report)))
		patterns := make([]string, len(runs))
		for i, args := range runs {
			patterns[i] = runArg(args)
			if args[len(args)-1] != "example.com/p" {
				t.Errorf("line %d: %v does not end with the package", tc.line, args)
			}
		}
		if !slices.Equal(patterns, tc.runs) || mut.Status() != mutator.Lived {
			t.Errorf("line %d: runs %v, %v; want %q, LIVED", tc.line, runs, mut.Status(), tc.runs)
		}
	}
}

// confirmCases are a subset's outcome, how every test answers, and the verdict that stands, with the
// share of kills confirmed, whether the subset fails on the unmutated code, the commands run, whether
// the verdict waits for Settle, and what the selector reports.
var confirmCases = []struct {
	name          string
	subset, whole execContext
	confirm       int
	failing       bool
	status        mutator.Status
	runs          int
	pending       bool
	heard         string
}{
	{"a survivor every test kills", fakeExecCommandSuccess, fakeExecCommandTestsFailure, 0, false, mutator.Killed, 2, false, ""},
	{"a survivor every test lets live", fakeExecCommandSuccess, fakeExecCommandSuccess, 0, false, mutator.Lived, 2, false, ""},
	{"a kill by a subset that fails unmutated", fakeExecCommandTestsFailure, fakeExecCommandSuccess, 0, true, mutator.Lived, 2, false, failTogether},
	{"a kill outside the sample", fakeExecCommandTestsFailure, fakeExecCommandSuccess, 0, false, mutator.Killed, 1, true, ""},
	{"a sampled kill every test agrees with", fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, 100, false, mutator.Killed, 2, false, ""},
	{"a sampled kill every test disagrees with", fakeExecCommandTestsFailure, fakeExecCommandSuccess, 100, false, mutator.Lived, 2, false, "SELECTION DISAGREES"},
	{"a subset that does not build", fakeExecCommandBuildFailure, fakeExecCommandSuccess, 0, false, mutator.NotViable, 1, false, ""},
	{"a time-out of a subset that hangs unmutated", binaryLate, fakeExecCommandSuccess, 0, true, mutator.Lived, 2, false, failTogether},
	{"a time-out of a subset that passes unmutated", binaryLate, fakeExecCommandSuccess, 0, false, mutator.TimedOut, 1, true, ""},
	{"a crash of a subset that fails unmutated", crashedBinary, fakeExecCommandSuccess, 0, true, mutator.Lived, 2, false, failTogether},
	{"a crash of a subset that passes unmutated", crashedBinary, fakeExecCommandSuccess, 0, false, mutator.Unclassified, 1, true, ""},
}

func TestASubsetsVerdictIsCheckedWithEveryTestWhereTheTwoCanDiffer(t *testing.T) {
	for _, tc := range confirmCases {
		t.Run(tc.name, func(t *testing.T) {
			got := &commands{subset: tc.subset, whole: tc.whole}
			h := &heard{}
			stub := &indexStub{failing: map[string]bool{testOne: tc.failing}}
			dealer := newDealer(t, got.exec, engine.WithSelector(selection.New(stub, tc.confirm, h.report)))
			mut := mutantAt(mutator.Runnable, 3)
			start(dealer, mut, "selection")
			runs := got.all()
			if mut.Status() != tc.status || len(runs) != tc.runs || runArg(runs[0]) != testOne || (tc.runs == 2 && runArg(runs[1]) != "") {
				t.Errorf("%v after %v, want %v after %d commands", mut.Status(), runs, tc.status, tc.runs)
			}
			if dealer.Pending(mut) != tc.pending {
				t.Errorf("pending %t", dealer.Pending(mut))
			}
			if heard := h.all(); (tc.heard == "") != (heard == "") || !strings.Contains(heard, tc.heard) {
				t.Errorf("heard %q, want %q", heard, tc.heard)
			}
		})
	}
}

func TestADisagreementMakesThePackageRunEveryTest(t *testing.T) {
	got := &commands{subset: fakeExecCommandTestsFailure, whole: fakeExecCommandSuccess}
	dealer := newDealer(t, got.exec, engine.WithSelector(selection.New(&indexStub{}, 100, (&heard{}).report)))
	start(dealer, mutantAt(mutator.Runnable, 3), "selection")
	start(dealer, mutantAt(mutator.Runnable, 4), "selection")
	if runs := got.all(); len(runs) != 3 || runArg(runs[2]) != "" {
		t.Fatalf("after the disagreement: %v", runs)
	}
}

func TestTheIndexIsNotChargedToTheMutantsTime(t *testing.T) {
	const elapsed = 500 * time.Millisecond
	budget := elapsed * engine.DefaultTimeoutCoefficient
	got := passing()
	sel := engine.WithSelector(selection.New(&indexStub{delay: budget + 500*time.Millisecond}, 0, (&heard{}).report))
	mutants := []*mutantStub{mutantAt(mutator.Runnable, 3), mutantAt(mutator.Runnable, 4)}
	var wg sync.WaitGroup
	for _, mut := range mutants {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMutant(t, mut, got, elapsed, sel)
		}()
	}
	wg.Wait()
	for _, mut := range mutants {
		if mut.Status() != mutator.Lived {
			t.Errorf("%v: %v", mut.Position(), mut.Status())
		}
	}
	for i, left := range got.timeouts {
		if left < budget/2 || left > budget {
			t.Errorf("%v had %v of its %v", got.runs[i], left, budget)
		}
	}
}

func TestAMutantNoTestCoversIsNeitherIndexedNorRun(t *testing.T) {
	stub := &indexStub{}
	mut := mutantAt(mutator.NotCovered, 4)
	if runs := runMutant(t, mut, passing(), expectedTimeout, engine.WithSelector(selection.New(stub, 0, (&heard{}).report))); len(runs) != 0 || stub.calls != 0 {
		t.Fatalf("commands %v, %d indexings", runs, stub.calls)
	}
	if mut.Status() != mutator.NotCovered {
		t.Fatalf("status %v", mut.Status())
	}
}

func TestIntegrationModeRunsEveryTest(t *testing.T) {
	viperSet(map[string]any{configuration.UnleashIntegrationMode: true})
	defer viperReset()
	stub := &indexStub{}
	runs := runMutant(t, mutantAt(mutator.Runnable, 4), passing(), expectedTimeout, engine.WithSelector(selection.New(stub, 0, (&heard{}).report)))
	if len(runs) != 1 || runArg(runs[0]) != "" || stub.calls != 0 {
		t.Fatalf("commands %v, %d indexings", runs, stub.calls)
	}
}

// settingCases are the settings of a run and the -run of the mutant's first command, in a
// fixture of internal/selection/testdata at a position one test covers.
var settingCases = []struct {
	name     string
	settings map[string]any
	fixture  string
	line     int
	run      string
}{
	{"on, two workers", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashWorkersKey: 2}, initmod, 25, "^(TestAdd)$"},
	{"on, the default workers", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashWorkersKey: 0}, initmod, 25, "^(TestAdd)$"},
	{"on, with build tags", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashTagsKey: "extra"}, "kinds", 31, "^(TestTagged)$"},
	{"on, a package that cannot be indexed", map[string]any{configuration.UnleashTestSelectionKey: true}, "orderdep", 7, ""},
	{"on, in integration mode", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashIntegrationMode: true}, initmod, 25, ""},
	{"off", map[string]any{configuration.UnleashTestSelectionKey: false}, initmod, 25, ""},
}

func TestTheTestSelectionSettingIndexesWithTheGoCommand(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	for _, tc := range settingCases {
		t.Run(tc.name, func(t *testing.T) {
			viperSet(tc.settings)
			defer viperReset()
			root, err := filepath.Abs(filepath.Join("..", "selection", "testdata", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			mod := gomodule.GoModule{Name: "example.com/" + tc.fixture, Root: root, CallingDir: "."}
			wdd := workdir.NewCachedDealer(t.TempDir(), root)
			defer wdd.Clean()
			got := &commands{subset: exec.CommandContext, whole: exec.CommandContext}
			mut := &mutantStub{status: mutator.Runnable, mutType: mutator.ArithmeticBase, pkg: mod.Name,
				position: token.Position{Filename: tc.fixture + ".go", Line: tc.line, Column: 11}}
			start(engine.NewExecutorDealer(mod, wdd, 30*time.Second, engine.WithExecContext(got.exec)), mut, "selection")
			var tests [][]string // the mutant's commands, not the index's
			for _, args := range got.all() {
				if args[0] == "go" && !slices.Contains(args, "-c") {
					tests = append(tests, args)
				}
			}
			if len(tests) == 0 || runArg(tests[0]) != tc.run || mut.Status() != mutator.Lived {
				t.Fatalf("%v after %v, want -run %q first", mut.Status(), got.all(), tc.run)
			}
		})
	}
}
