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
	"time"

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

const packageFailed = `{"Action":"fail","Package":"example.com/p","Elapsed":2}` + "\n"

var (
	// binaryLate is a test binary past its own -timeout.
	binaryLate = printing(`{"Action":"output","Package":"example.com/p","Test":"TestOne","Output":"panic: test timed out after 3s\n"}`+"\n"+packageFailed, 1)
	// crashedBinary is a test binary a signal ended.
	crashedBinary = printing(`{"Action":"output","Package":"example.com/p","Test":"TestOne","Output":"signal: killed\n"}`+"\n"+packageFailed, 1)
	// flagRefused is the go command refusing a flag.
	flagRefused = printing("invalid value \"x\" for flag -timeout: parse error\n", 2)
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

func (s *sequence) all() [][]string {
	s.m.Lock()
	defer s.m.Unlock()

	return slices.Clone(s.args)
}

// aloneDealer is a dealer running the commands of seq, and the names of the work directories it
// asked for; fail, when set, makes the work directory of that name fail.
func aloneDealer(t *testing.T, seq *sequence, fail string, opts ...engine.ExecutorDealerOption) (*engine.MutantExecutorDealer, *[]string) {
	t.Helper()
	var dirs []string
	wdd := &dealerStub{t: t, fnGet: func(name string) (string, error) {
		dirs = append(dirs, name)
		if name == fail {
			return "", errors.New("no copy")
		}

		return t.TempDir(), nil
	}}
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}

	return engine.NewExecutorDealer(mod, tempDealer{wdd}, expectedTimeout, append([]engine.ExecutorDealerOption{engine.WithExecContext(seq.exec)}, opts...)...), &dirs
}

func TestARunsEventsDecideItsVerdict(t *testing.T) {
	cases := []struct {
		name    string
		run     execContext
		status  mutator.Status
		pending bool
	}{
		{"every test passed", fakeExecCommandSuccess, mutator.Lived, false},
		{"a test failed", fakeExecCommandTestsFailure, mutator.Killed, false},
		{"the mutant does not build", fakeExecCommandBuildFailure, mutator.NotViable, false},
		{"the test binary passed its -timeout", binaryLate, mutator.TimedOut, true},
		{"a signal ended the test binary", crashedBinary, mutator.Unclassified, true},
		{"the go command refused a flag", flagRefused, mutator.Unclassified, false},
		{"the go command did not start", notStarted, mutator.Unclassified, false},
	}
	for _, tc := range cases {
		dealer, _ := aloneDealer(t, &sequence{runs: []execContext{tc.run}}, "")
		mut := mutantAt(mutator.Runnable, 3)
		start(dealer, mut, "w")
		if mut.Status() != tc.status || dealer.Pending(mut) != tc.pending {
			t.Errorf("%s: %v, pending %t; want %v, %t", tc.name, mut.Status(), dealer.Pending(mut), tc.status, tc.pending)
		}
	}
}

func TestAMutantThatCannotBeAppliedIsUnclassified(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
	dealer, _ := aloneDealer(t, seq, "")
	mut := mutantAt(mutator.Runnable, 3)
	mut.hasApplyError = true
	start(dealer, mut, "w")
	if mut.Status() != mutator.Unclassified || dealer.Pending(mut) || len(seq.all()) != 0 {
		t.Fatalf("%v, pending %t, after %v", mut.Status(), dealer.Pending(mut), seq.all())
	}
}

func TestAMutantThatCannotBeRestoredKeepsItsVerdict(t *testing.T) {
	dealer, _ := aloneDealer(t, &sequence{runs: []execContext{fakeExecCommandTestsFailure}}, "")
	mut := mutantAt(mutator.Runnable, 3)
	mut.hasRollbackError = true
	start(dealer, mut, "w")
	if mut.Status() != mutator.Killed || !mut.rollbackCalled {
		t.Fatalf("%v, rolled back %t", mut.Status(), mut.rollbackCalled)
	}
}

func TestAWorkerWithoutAWorkDirectoryStops(t *testing.T) {
	dealer, _ := aloneDealer(t, &sequence{runs: []execContext{binaryLate}}, "alone")
	mut := mutantAt(mutator.Runnable, 3)
	start(dealer, mut, "w")
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "no copy") {
			t.Fatalf("recovered %v", r)
		}
	}()
	dealer.Settle(context.Background(), []mutator.Mutator{mut})
}

// aloneRuns are a mutant's first run, which needs a run alone, its run alone, and the verdict.
var aloneRuns = []struct {
	name         string
	first, alone execContext
	want         mutator.Status
}{
	{"a time-out that repeats", binaryLate, binaryLate, mutator.TimedOut},
	{"a time-out, then a pass", binaryLate, fakeExecCommandSuccess, mutator.Lived},
	{"a time-out, then a failed test", binaryLate, fakeExecCommandTestsFailure, mutator.Killed},
	{"a time-out, then a crash", binaryLate, crashedBinary, mutator.Unclassified},
	{"a crash that repeats", crashedBinary, crashedBinary, mutator.Killed},
	{"a crash, then a pass", crashedBinary, fakeExecCommandSuccess, mutator.Lived},
	{"a crash, then a build failure", crashedBinary, fakeExecCommandBuildFailure, mutator.NotViable},
	{"a crash, then a run that does not start", crashedBinary, notStarted, mutator.Unclassified},
}

func TestATimeOutOrACrashRunsAgainAloneWithEveryTest(t *testing.T) {
	for _, tc := range aloneRuns {
		t.Run(tc.name, func(t *testing.T) {
			seq := &sequence{runs: []execContext{tc.first, tc.alone}}
			dealer, dirs := aloneDealer(t, seq, "", engine.WithSelector(selection.New(&indexStub{}, 0, (&heard{}).report)))
			mut := mutantAt(mutator.Runnable, 3)
			start(dealer, mut, "w")
			if !dealer.Pending(mut) || len(seq.all()) != 1 || runArg(seq.all()[0]) != testOne {
				t.Fatalf("first: pending %t after %v", dealer.Pending(mut), seq.all())
			}
			dealer.Settle(context.Background(), []mutator.Mutator{mut})
			runs := seq.all()
			if mut.Status() != tc.want || len(runs) != 2 || runArg(runs[1]) != "" || (*dirs)[len(*dirs)-1] != "alone" {
				t.Errorf("%v after %v in %v, want %v after every test in alone", mut.Status(), runs, *dirs, tc.want)
			}
		})
	}
}

// killCases are two subset kills outside the sample, at lines 3 (^(TestOne)$, the package's
// lowest kill) and 8 (^(TestTwo)$), started in order, the commands their runs and Settle's take in
// turn, whether ^(TestTwo)$ fails on the unmutated code, and the verdicts at lines 3 and 8.
var killCases = []struct {
	name    string
	order   []int
	runs    []execContext
	failing bool
	want3   mutator.Status
	want8   mutator.Status
	count   int
}{
	{"the lowest kill agrees with every test", []int{8, 3},
		[]execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, fakeExecCommandTestsFailure},
		false, mutator.Killed, mutator.Killed, 3},
	{"the lowest kill disagrees, so every kill is confirmed", []int{8, 3},
		[]execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, fakeExecCommandSuccess, fakeExecCommandSuccess},
		false, mutator.Lived, mutator.Lived, 4},
	{"the lowest kill, which ended last, disagrees while another agrees", []int{8, 3},
		[]execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, fakeExecCommandSuccess, fakeExecCommandTestsFailure},
		false, mutator.Lived, mutator.Killed, 4},
	{"a kill by a subset that fails unmutated, beside the lowest kill", []int{3, 8},
		[]execContext{fakeExecCommandTestsFailure, fakeExecCommandTestsFailure, fakeExecCommandSuccess, fakeExecCommandTestsFailure},
		true, mutator.Killed, mutator.Lived, 4},
	{"the same, in the other order", []int{8, 3},
		[]execContext{fakeExecCommandTestsFailure, fakeExecCommandSuccess, fakeExecCommandTestsFailure, fakeExecCommandTestsFailure},
		true, mutator.Killed, mutator.Lived, 4},
}

func TestSettleConfirmsEachPackagesLowestKill(t *testing.T) {
	for _, tc := range killCases {
		t.Run(tc.name, func(t *testing.T) {
			seq := &sequence{runs: tc.runs}
			stub := &indexStub{failing: map[string]bool{"^(TestTwo)$": tc.failing}}
			dealer, _ := aloneDealer(t, seq, "", engine.WithSelector(selection.New(stub, 0, (&heard{}).report)))
			muts := map[int]*mutantStub{3: mutantAt(mutator.Runnable, 3), 8: mutantAt(mutator.Runnable, 8)}
			var pending []mutator.Mutator
			for _, line := range tc.order {
				start(dealer, muts[line], "w")
				if dealer.Pending(muts[line]) {
					pending = append(pending, muts[line])
				}
			}
			dealer.Settle(context.Background(), pending)
			if muts[3].Status() != tc.want3 || muts[8].Status() != tc.want8 || len(seq.all()) != tc.count {
				t.Errorf("%v, %v after %v; want %v, %v after %d", muts[3].Status(), muts[8].Status(), seq.all(), tc.want3, tc.want8, tc.count)
			}
		})
	}
}

func TestACancelledSettleLeavesItsMutantsUnclassified(t *testing.T) {
	seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure, binaryLate}}
	dealer, _ := aloneDealer(t, seq, "", engine.WithSelector(selection.New(&indexStub{}, 0, (&heard{}).report)))
	kill, late := mutantAt(mutator.Runnable, 3), mutantAt(mutator.Runnable, 8)
	start(dealer, kill, "w")
	start(dealer, late, "w")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dealer.Settle(ctx, []mutator.Mutator{kill, late})
	if kill.Status() != mutator.Unclassified || late.Status() != mutator.Unclassified || len(seq.all()) != 2 {
		t.Fatalf("%v, %v after %v", kill.Status(), late.Status(), seq.all())
	}
}

// settlerStub runs each mutant as executorDealerStub does, holds every one for Settle, and
// settles each as KILLED.
type settlerStub struct {
	executorDealerStub
	settled [][]mutator.Mutator
	seen    []int // how many mutants had run when Settle was called
}

func (*settlerStub) Pending(mutator.Mutator) bool { return true }

func (d *settlerStub) Settle(_ context.Context, muts []mutator.Mutator) {
	d.settled = append(d.settled, muts)
	d.seen = append(d.seen, len(d.gotMutants))
	for _, mut := range muts {
		mut.SetStatus(mutator.Killed)
	}
}

func TestTheEngineSettlesThePendingMutantsOnceEveryMutantHasRun(t *testing.T) {
	mapFS, mod, c := loadFixture(defaultFixture, ".")
	defer c()
	d := &settlerStub{}
	mut := engine.New(mod, testCodeData, d, engine.WithDirFs(mapFS))
	res := mut.Run(context.Background())
	if len(res.Mutants) < 2 || len(d.settled) != 1 || len(d.settled[0]) != len(res.Mutants) || d.seen[0] != len(res.Mutants) {
		t.Fatalf("%d mutants; settled %d times, after %v", len(res.Mutants), len(d.settled), d.seen)
	}
	for _, m := range res.Mutants {
		if m.Status() != mutator.Killed || !slices.Contains(d.settled[0], m) {
			t.Errorf("%v: %v", m.Position(), m.Status())
		}
	}
}

// TestProcessSpawns starts a child that sleeps, writes its pid to GREMLINS_PIDFILE and sleeps, as a
// go command does whose test binary hangs.
func TestProcessSpawns(*testing.T) {
	if os.Getenv("GO_TEST_PROCESS") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProcessSleeps$") //nolint:gosec // this test binary
	if child.Start() != nil {
		os.Exit(3) // skipcq: RVV-A0003
	}
	pidFile := os.Getenv("GREMLINS_PIDFILE")
	if os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600) != nil { //nolint:gosec // the file the test names
		os.Exit(3) // skipcq: RVV-A0003
	}
	time.Sleep(time.Minute)
}

// TestProcessSleeps sleeps.
func TestProcessSleeps(*testing.T) {
	if os.Getenv("GO_TEST_PROCESS") == "1" {
		time.Sleep(time.Minute)
	}
}

func TestATimedOutRunEndsEveryProcessItStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	spawn := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := getCmd(ctx, []string{"-test.run=^TestProcessSpawns$"})
		cmd.Env = append(cmd.Env, "GREMLINS_PIDFILE="+pidFile)

		return cmd
	}
	mut := mutantAt(mutator.Runnable, 3)
	runMutant(t, mut, &commands{subset: spawn, whole: spawn}, time.Second)
	pid, err := os.ReadFile(pidFile) //nolint:gosec // a file of this test's temporary directory
	if mut.Status() != mutator.TimedOut || err != nil {
		t.Fatalf("%v, the child's pid: %v", mut.Status(), err)
	}
	n, _ := strconv.Atoi(string(pid))
	for wait := time.Now().Add(5 * time.Second); alive(n); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(wait) {
			t.Fatalf("the child %d of the timed-out run still runs", n)
		}
	}
}
