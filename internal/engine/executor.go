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

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine/workdir"
	"github.com/go-gremlins/gremlins/internal/engine/workerpool"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// DefaultTimeoutCoefficient is the default multiplier for the timeout length
// of each test run.
const DefaultTimeoutCoefficient = 3

// ExecutorDealer is the initializer for new workerpool.Executor.
type ExecutorDealer interface {
	NewExecutor(mut mutator.Mutator, outCh chan<- mutator.Mutator, wg *sync.WaitGroup) workerpool.Executor
}

// MutantExecutorDealer is a ExecutorDealer for the initialisation of a mutantExecutor.
//
// By default, it sets uses exec.Command to perform the tests on the source
// code. This can be overridden, for example in tests.
//
// The apply and rollback functions are wrappers around the TokenMutator apply and
// rollback. These can be overridden with nop functions in tests. Not an
// ideal setup. In the future we can think of a better way to handle this.
type MutantExecutorDealer struct {
	wdDealer          workdir.Dealer
	execContext       execContext
	mod               gomodule.GoModule
	buildTags         string
	testExecutionTime time.Duration
	selector          *selection.Selector
	dryRun            bool
	integrationMode   bool
	testCPU           int
}

// ExecutorDealerOption is the defining option for the initialisation of a ExecutorDealer.
type ExecutorDealerOption func(j MutantExecutorDealer) MutantExecutorDealer

// WithExecContext overrides the default exec.Command with a custom executor.
func WithExecContext(c execContext) ExecutorDealerOption {
	return func(m MutantExecutorDealer) MutantExecutorDealer {
		m.execContext = c

		return m
	}
}

// WithSelector makes each mutant run only the tests the selector chooses for it.
func WithSelector(s *selection.Selector) ExecutorDealerOption {
	return func(m MutantExecutorDealer) MutantExecutorDealer {
		m.selector = s

		return m
	}
}

// NewExecutorDealer initialises a MutantExecutorDealer. With test selection on (and integration
// mode off), each mutant runs only the tests of its package that cover its position
// (package selection).
func NewExecutorDealer(mod gomodule.GoModule, wdd workdir.Dealer, elapsed time.Duration, opts ...ExecutorDealerOption) *MutantExecutorDealer {
	buildTags := configuration.Get[string](configuration.UnleashTagsKey)
	dryRun := configuration.Get[bool](configuration.UnleashDryRunKey)
	integrationMode := configuration.Get[bool](configuration.UnleashIntegrationMode)
	testCPU := configuration.Get[int](configuration.UnleashTestCPUKey)
	tCoefficient := configuration.Get[int](configuration.UnleashTimeoutCoefficientKey)

	coefficient := DefaultTimeoutCoefficient
	if tCoefficient != 0 {
		coefficient = tCoefficient
	}

	if testCPU != 0 && integrationMode {
		testCPU /= testCPU
	}

	jd := MutantExecutorDealer{
		mod:               mod,
		wdDealer:          wdd,
		buildTags:         buildTags,
		dryRun:            dryRun,
		integrationMode:   integrationMode,
		testCPU:           testCPU,
		testExecutionTime: elapsed * time.Duration(coefficient),
		execContext:       exec.CommandContext,
	}

	for _, opt := range opts {
		jd = opt(jd)
	}
	if jd.selector == nil && configuration.Get[bool](configuration.UnleashTestSelectionKey) && !integrationMode {
		jd.selector = newSelector(jd.execContext, mod, wdd.WorkDir(), buildTags, jd.testExecutionTime)
	}

	return &jd
}

// newSelector is the selector of the module's tests, building each package's index with the go
// command, as many tests at once as there are workers.
func newSelector(ec execContext, mod gomodule.GoModule, workDir, buildTags string, timeout time.Duration) *selection.Selector {
	workers := configuration.Get[int](configuration.UnleashWorkersKey)
	if workers == 0 {
		workers = runtime.NumCPU()
	}
	builder := selection.GoBuilder{
		Exec: ec, Module: mod, WorkDir: workDir, Tags: buildTags,
		Parallelism: workers, Timeout: timeout,
	}

	return selection.New(builder, func(pkg string, err error) {
		log.Errorf("test selection: every test of %s runs for each of its mutants: %v\n", pkg, err)
	})
}

// NewExecutor returns a new workerpool.Executor for the given mutator.Mutator.
// It gets an output channel of mutator.Mutator and a sync.WaitGroup. The channel
// will stream the results of the executor, and the wait group will be done when the
// executor is complete.
func (m MutantExecutorDealer) NewExecutor(mut mutator.Mutator, outCh chan<- mutator.Mutator, wg *sync.WaitGroup) workerpool.Executor {
	mj := mutantExecutor{
		mutant:            mut,
		outCh:             outCh,
		wg:                wg,
		wdDealer:          m.wdDealer,
		module:            m.mod,
		dryRun:            m.dryRun,
		integrationMode:   m.integrationMode,
		buildTags:         m.buildTags,
		execContext:       m.execContext,
		testCPU:           m.testCPU,
		testExecutionTime: m.testExecutionTime,
		selector:          m.selector,
	}

	return &mj
}

type execContext = func(ctx context.Context, name string, args ...string) *exec.Cmd

type mutantExecutor struct {
	mutant            mutator.Mutator
	wdDealer          workdir.Dealer
	outCh             chan<- mutator.Mutator
	wg                *sync.WaitGroup
	execContext       execContext
	module            gomodule.GoModule
	buildTags         string
	testExecutionTime time.Duration
	selector          *selection.Selector
	dryRun            bool
	integrationMode   bool
	testCPU           int
}

// Start is the implementation of the workerpool.Executor definition and is the
// method responsible for performing the actual mutation testing.
// The executor runs on its mutator.Mutator.
// If it is RUNNABLE, and it is not in dry-run mode, it will apply the mutation,
// run the tests and mark the TokenMutator as either KILLED or LIVED depending
// on the result. If the tests pass, it means the TokenMutator survived, so it
// will be LIVED, if the tests fail, the TokenMutator will be KILLED.
// The timeout of the test is managed outside the run of the test, using
// a context with timeout. This is done because the Go test command doesn't
// make it easy to distinguish failures from timeouts.
func (m *mutantExecutor) Start(w *workerpool.Worker) {
	defer m.wg.Done()
	workerName := fmt.Sprintf("%s-%d", w.Name, w.ID)
	rootDir, err := m.wdDealer.Get(workerName)
	if err != nil {
		log.Errorf("failed to get working directory for worker %s: %v", workerName, err)
		panic(fmt.Sprintf("failed to get working directory for worker %s: %v", workerName, err))
	}

	workingDir := filepath.Join(rootDir, m.module.CallingDir)
	m.mutant.SetWorkdir(workingDir)

	if m.mutant.Status() == mutator.NotCovered || m.mutant.Status() == mutator.Skipped || m.dryRun {
		m.outCh <- m.mutant

		return
	}

	if err := m.mutant.Apply(); err != nil {
		log.Errorf("failed to apply mutation at %s - %s\n\t%v", m.mutant.Position(), m.mutant.Status(), err)

		return
	}

	m.mutant.SetStatus(m.runTests(rootDir, m.mutant.Pkg()))

	if err := m.mutant.Rollback(); err != nil {
		// What should we do now?
		log.Errorf("failed to restore mutation at %s - %s\n\t%v", m.mutant.Position(), m.mutant.Status(), err)
	}

	m.outCh <- m.mutant
}

func (m *mutantExecutor) runTests(rootDir, pkg string) mutator.Status {
	ctx, cancel := context.WithTimeout(context.Background(), m.testExecutionTime)
	defer cancel()

	cmd := m.execContext(ctx, "go", m.getTestArgs(pkg, m.selectedTests(pkg))...)
	cmd.Dir = m.mutant.Workdir()
	if m.integrationMode {
		cmd.Dir = rootDir
	}
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOTMPDIR=%s", m.wdDealer.WorkDir()))

	rel, err := run(cmd)
	defer rel()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return mutator.TimedOut
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return getTestFailedStatus(exitErr.ExitCode())
	}

	return mutator.Lived
}

// selectedTests is the -run pattern of the tests of pkg that cover the mutant's position, or ""
// for every test: without a selector, in integration mode, or when no single test covers it.
func (m *mutantExecutor) selectedTests(pkg string) string {
	if m.selector == nil || m.integrationMode {
		return ""
	}
	pattern, _ := m.selector.Pattern(pkg, m.mutant.Position())

	return pattern
}

func (m *mutantExecutor) getTestArgs(pkg, run string) []string {
	args := []string{"test"}
	if m.buildTags != "" {
		args = append(args, "-tags", m.buildTags)
	}
	// Here we add some seconds to the timeout to be sure it's gremlins that catches the test
	// timeout and not the test itself. The timeout on the test prevents the test.* processes
	// from hanging forever.
	args = append(args, "-timeout", (2*time.Second + m.testExecutionTime).String())
	args = append(args, "-failfast")

	if m.testCPU != 0 {
		args = append(args, "-cpu", strconv.Itoa(m.testCPU))
	}
	if run != "" {
		args = append(args, "-run", run)
	}

	path := pkg
	if m.integrationMode {
		path = "./..."
	}
	args = append(args, path)

	return args
}

func run(cmd *exec.Cmd) (func(), error) {
	if err := cmd.Run(); err != nil {

		return func() {}, err
	}

	return func() {
		err := cmd.Process.Release()
		if err != nil {
			_ = cmd.Process.Kill()
		}
	}, nil
}

func getTestFailedStatus(exitCode int) mutator.Status {
	switch exitCode {
	case 1:
		return mutator.Killed
	case 2:
		return mutator.NotViable
	default:
		return mutator.Lived
	}
}
