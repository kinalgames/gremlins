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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// TestProcessPrints prints GREMLINS_OUTPUT and exits with GREMLINS_EXIT, as a go test run does.
func TestProcessPrints(*testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("GREMLINS_OUTPUT"))
	code, _ := strconv.Atoi(os.Getenv("GREMLINS_EXIT"))
	os.Exit(code) // skipcq: RVV-A0003
}

// printing is a go test run that prints output and exits with code.
func printing(output string, code int) execContext {
	return func(ctx context.Context, command string, args ...string) *exec.Cmd {
		cmd := getCmd(ctx, append([]string{"-test.run=^TestProcessPrints$", "--", command}, args...))
		cmd.Env = append(cmd.Env, "GREMLINS_OUTPUT="+output, "GREMLINS_EXIT="+strconv.Itoa(code))

		return cmd
	}
}

// notStarted is a run whose command does not exist.
func notStarted(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, filepath.Join(os.TempDir(), "gremlins-no-such-command")) //nolint:gosec // a path that names no file
}

var (
	testFailed     = printing("--- FAIL: TestOne (0.00s)\nFAIL\n", 1)
	buildFailedRun = printing("FAIL\texample.com/p [build failed]\nFAIL\n", 1)
	testTimedOut   = printing("panic: test timed out after 3s\nFAIL\texample.com/p\t3.0s\n", 1)
	signalled      = printing("signal: killed\nFAIL\texample.com/p\t2.0s\n", 1)
)

// sequence runs its commands in turn, the last for every command after it, and records them.
type sequence struct {
	m    sync.Mutex
	runs []execContext
	args [][]string
}

func (s *sequence) exec(ctx context.Context, command string, args ...string) *exec.Cmd {
	s.m.Lock()
	defer s.m.Unlock()
	i := min(len(s.args), len(s.runs)-1)
	s.args = append(s.args, args)

	return s.runs[i](ctx, command, args...)
}

// aloneDealer is a dealer running the commands of seq, and the names of the work directories it
// asked for.
func aloneDealer(t *testing.T, seq *sequence, opts ...engine.ExecutorDealerOption) (*engine.MutantExecutorDealer, *[]string) {
	t.Helper()
	var dirs []string
	wdd := &dealerStub{t: t, fnGet: func(name string) (string, error) {
		dirs = append(dirs, name)

		return t.TempDir(), nil
	}}
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}

	return engine.NewExecutorDealer(mod, tempDealer{wdd}, expectedTimeout, append([]engine.ExecutorDealerOption{engine.WithExecContext(seq.exec)}, opts...)...), &dirs
}

func TestARunsOutputDecidesItsVerdict(t *testing.T) {
	cases := []struct {
		name   string
		run    execContext
		status mutator.Status
		again  bool
	}{
		{"every test passed", fakeExecCommandSuccess, mutator.Lived, false},
		{"a test failed", testFailed, mutator.Killed, false},
		{"the mutant does not build", buildFailedRun, mutator.NotViable, false},
		{"the test binary passed its -timeout", testTimedOut, mutator.TimedOut, true},
		{"a signal ended the test binary", signalled, mutator.Unclassified, true},
		{"the go command did not start", notStarted, mutator.Unclassified, true},
	}
	for _, tc := range cases {
		dealer, _ := aloneDealer(t, &sequence{runs: []execContext{tc.run}})
		mut := mutantAt(mutator.Runnable, 3)
		start(dealer, mut, "w")
		if mut.Status() != tc.status || dealer.Again(mut) != tc.again {
			t.Errorf("%s: %v, again %t; want %v, %t", tc.name, mut.Status(), dealer.Again(mut), tc.status, tc.again)
		}
	}
}

func TestAMutantThatCannotBeAppliedRunsAgainAlone(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
	dealer, _ := aloneDealer(t, seq)
	mut := mutantAt(mutator.Runnable, 3)
	mut.hasApplyError = true
	start(dealer, mut, "w")
	if mut.Status() != mutator.Unclassified || !dealer.Again(mut) || len(seq.args) != 0 {
		t.Fatalf("%v, again %t, after %v", mut.Status(), dealer.Again(mut), seq.args)
	}
}

func TestAMutantThatCannotBeRestoredKeepsItsVerdict(t *testing.T) {
	dealer, _ := aloneDealer(t, &sequence{runs: []execContext{testFailed}})
	mut := mutantAt(mutator.Runnable, 3)
	mut.hasRollbackError = true
	start(dealer, mut, "w")
	if mut.Status() != mutator.Killed || !mut.rollbackCalled {
		t.Fatalf("%v, rolled back %t", mut.Status(), mut.rollbackCalled)
	}
}

func TestAWorkerWithoutAWorkDirectoryStops(t *testing.T) {
	wdd := &dealerStub{t: t, fnGet: func(string) (string, error) { return "", errors.New("no copy") }}
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}
	dealer := engine.NewExecutorDealer(mod, tempDealer{wdd}, expectedTimeout, engine.WithExecContext(fakeExecCommandSuccess))
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "no copy") {
			t.Fatalf("recovered %v", r)
		}
	}()
	dealer.RunAlone(mutantAt(mutator.Runnable, 3))
}

// aloneRuns are a mutant's first run, which needs a run alone, its run alone, and the verdict.
var aloneRuns = []struct {
	name         string
	first, alone execContext
	want         mutator.Status
}{
	{"a time-out that repeats", testTimedOut, testTimedOut, mutator.TimedOut},
	{"a time-out, then a pass", testTimedOut, fakeExecCommandSuccess, mutator.Lived},
	{"a time-out, then a failed test", testTimedOut, testFailed, mutator.Killed},
	{"a time-out, then a signal", testTimedOut, signalled, mutator.Unclassified},
	{"a signal that repeats", signalled, signalled, mutator.Killed},
	{"a signal, then a pass", signalled, fakeExecCommandSuccess, mutator.Lived},
	{"a run that did not start, then none", notStarted, notStarted, mutator.Unclassified},
}

func TestAMutantRunsAgainAloneWithEveryTestInItsOwnDirectory(t *testing.T) {
	for _, tc := range aloneRuns {
		t.Run(tc.name, func(t *testing.T) {
			seq := &sequence{runs: []execContext{tc.first, tc.alone}}
			dealer, dirs := aloneDealer(t, seq, engine.WithSelector(selection.New(&indexStub{}, 0, (&heard{}).report)))
			mut := mutantAt(mutator.Runnable, 3)
			start(dealer, mut, "w")
			if !dealer.Again(mut) || len(seq.args) != 1 || runArg(seq.args[0]) != testOne {
				t.Fatalf("first: again %t after %v", dealer.Again(mut), seq.args)
			}
			dealer.RunAlone(mut)
			if mut.Status() != tc.want || len(seq.args) != 2 || runArg(seq.args[1]) != "" || (*dirs)[len(*dirs)-1] != "alone" {
				t.Errorf("%v after %v in %v, want %v after every test in alone", mut.Status(), seq.args, *dirs, tc.want)
			}
		})
	}
}

// aloneDealerStub runs each mutant as executorDealerStub does, then has every one run again alone,
// where it is KILLED; cancel, when set, is called on the first run alone.
type aloneDealerStub struct {
	executorDealerStub
	alone  []mutator.Mutator
	seen   []int // how many mutants had run when each ran alone
	cancel func()
}

func (*aloneDealerStub) Again(mutator.Mutator) bool { return true }

func (d *aloneDealerStub) RunAlone(mut mutator.Mutator) {
	d.alone = append(d.alone, mut)
	d.seen = append(d.seen, len(d.gotMutants))
	mut.SetStatus(mutator.Killed)
	if d.cancel != nil {
		d.cancel()
	}
}

func TestTheEngineRunsTheMutantsAloneOnceEveryMutantHasRun(t *testing.T) {
	mapFS, mod, c := loadFixture(defaultFixture, ".")
	defer c()
	d := &aloneDealerStub{}
	mut := engine.New(mod, testCodeData, d, engine.WithDirFs(mapFS))
	res := mut.Run(context.Background())
	if len(res.Mutants) < 2 || len(d.alone) != len(res.Mutants) {
		t.Fatalf("%d mutants, %d alone", len(res.Mutants), len(d.alone))
	}
	for i, m := range res.Mutants {
		if m.Status() != mutator.Killed || d.seen[i] != len(res.Mutants) || !slices.Contains(d.alone, m) {
			t.Errorf("%v: %v, alone after %d of %d", m.Position(), m.Status(), d.seen[i], len(res.Mutants))
		}
	}
}

func TestACancelledRunLeavesTheMutantsNotRunAloneUnclassified(t *testing.T) {
	mapFS, mod, c := loadFixture(defaultFixture, ".")
	defer c()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &aloneDealerStub{cancel: cancel}
	mut := engine.New(mod, testCodeData, d, engine.WithDirFs(mapFS))
	res := mut.Run(ctx)
	if len(res.Mutants) < 2 || len(d.alone) != 1 {
		t.Fatalf("%d mutants, %d alone", len(res.Mutants), len(d.alone))
	}
	for _, m := range res.Mutants {
		want := mutator.Unclassified
		if m == d.alone[0] {
			want = mutator.Killed
		}
		if m.Status() != want {
			t.Errorf("%v: %v, want %v", m.Position(), m.Status(), want)
		}
	}
}
