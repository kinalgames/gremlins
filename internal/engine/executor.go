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
	"bytes"
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
	unsettled         *unsettled
	copies            *copies
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
		unsettled:         newUnsettled(),
		copies:            &copies{spoiled: map[string]error{}},
	}

	for _, opt := range opts {
		jd = opt(jd)
	}
	if jd.selector == nil && configuration.Get[bool](configuration.UnleashTestSelectionKey) && !integrationMode {
		jd.selector = newSelector(jd.execContext, mod, wdd, buildTags, jd.testExecutionTime)
	}

	return &jd
}

// waitDelay bounds how long a run's output is awaited after it ends or is stopped.
const waitDelay = 5 * time.Second

// selectionTree is the work directory of the copy of the module the selection's tests run in,
// never the source: the workers copy the source while the first packages are indexed.
const selectionTree = "test-selection"

// newSelector is the selector of the module's tests, building each package's index with the go
// command, as many tests at once as there are workers.
func newSelector(ec execContext, mod gomodule.GoModule, wdd workdir.Dealer, buildTags string, timeout time.Duration) *selection.Selector {
	workers := configuration.Get[int](configuration.UnleashWorkersKey)
	if workers == 0 {
		workers = runtime.NumCPU()
	}
	builder := selection.GoBuilder{
		Exec: ec, Module: mod, WorkDir: wdd.WorkDir(), Tags: buildTags,
		Tree:        func() (string, error) { return wdd.Get(selectionTree) },
		Parallelism: workers, Timeout: timeout,
	}
	confirm := configuration.Get[int](configuration.UnleashSelectionConfirmKey)

	return selection.New(builder, confirm, reportSelection)
}

// reportSelection logs what a selector reports of pkg.
func reportSelection(pkg string, err error) {
	log.Errorf("test selection, %s: %v\n", pkg, err)
}

// NewExecutor returns a new workerpool.Executor for the given mutator.Mutator.
// It gets an output channel of mutator.Mutator and a sync.WaitGroup. The channel
// will stream the results of the executor, and the wait group will be done when the
// executor is complete.
func (m MutantExecutorDealer) NewExecutor(mut mutator.Mutator, outCh chan<- mutator.Mutator, wg *sync.WaitGroup) workerpool.Executor {
	return m.executor(mut, outCh, wg)
}

// executor is the mutantExecutor of mut.
func (m MutantExecutorDealer) executor(mut mutator.Mutator, outCh chan<- mutator.Mutator, wg *sync.WaitGroup) *mutantExecutor {
	return &mutantExecutor{
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
		unsettled:         m.unsettled,
		copies:            m.copies,
	}
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
	unsettled         *unsettled
	copies            *copies
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
// make it easy to distinguish failures from timeouts. A run that timed out or
// crashed is recorded to run again alone (Settler).
func (m *mutantExecutor) Start(w *workerpool.Worker) {
	defer m.wg.Done()
	name := fmt.Sprintf("%s-%d", w.Name, w.ID)
	rootDir := m.workdir(name)

	if m.mutant.Status() == mutator.NotCovered || m.mutant.Status() == mutator.Skipped || m.dryRun {
		m.outCh <- m.mutant

		return
	}

	o := broken
	if m.holdsSource(name) {
		o = m.test(name, func() outcome { return m.runTests(rootDir, m.mutant.Pkg()) })
	}
	if o.again() {
		m.unsettled.runAgain(m.mutant, o)
	}
	m.mutant.SetStatus(o.status())
	m.outCh <- m.mutant
}

// workdir is the root of the copy of the module the work directory name holds, where the mutant
// is applied.
func (m *mutantExecutor) workdir(name string) string {
	rootDir, err := m.wdDealer.Get(name)
	if err != nil {
		log.Errorf("failed to get working directory for worker %s: %v", name, err)
		panic(fmt.Sprintf("failed to get working directory for worker %s: %v", name, err))
	}
	m.mutant.SetWorkdir(filepath.Join(rootDir, m.module.CallingDir))

	return rootDir
}

// test applies the mutant to the copy of the work directory name, runs run on it and restores the
// code. A mutant that cannot be applied ends broken. A copy the mutant cannot be restored in is
// replaced with a fresh copy of the source (renew).
func (m *mutantExecutor) test(name string, run func() outcome) outcome {
	if err := m.mutant.Apply(); err != nil {
		log.Errorf("failed to apply mutation at %s - %s\n\t%v\n", m.mutant.Position(), m.mutant.Status(), err)

		return broken
	}
	o := run()
	if err := m.mutant.Rollback(); err != nil {
		log.Errorf("failed to restore mutation at %s - %s\n\t%v\n", m.mutant.Position(), m.mutant.Status(), err)
		m.renew(name)
	}

	return o
}

// copies are the work directories whose copy no longer holds the source, each with the reason.
type copies struct {
	mu      sync.Mutex
	spoiled map[string]error
}

func (c *copies) spoil(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spoiled[name] = err
}

func (c *copies) reason(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.spoiled[name]
}

// renew replaces the copy of the work directory name, where a mutant could not be restored, with a
// fresh copy of the source whose mutated file matches the source's. When that fails, the copy's
// later mutants run no test (holdsSource).
func (m *mutantExecutor) renew(name string) {
	root, err := m.wdDealer.Renew(name)
	if err == nil {
		err = m.sameAsSource(root)
	}
	if err != nil {
		m.copies.spoil(name, err)
		log.Errorf("failed to renew the copy of %s, so its later mutants are UNCLASSIFIED\n\t%v\n", name, err)
	}
}

// sameAsSource checks that the mutant's file in the copy at root holds the source's bytes.
func (m *mutantExecutor) sameAsSource(root string) error {
	name := filepath.Join(m.module.CallingDir, m.mutant.Position().Filename)
	src, err := os.ReadFile(filepath.Join(m.module.Root, name)) //nolint:gosec // a file of the module under test
	if err != nil {
		return err
	}
	copied, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // the same file in a copy of the module
	if err != nil {
		return err
	}
	if !bytes.Equal(src, copied) {
		return fmt.Errorf("%s differs from the source", filepath.Join(root, name))
	}

	return nil
}

// holdsSource reports whether the copy of the work directory name holds the source; when it does
// not, the mutant is UNCLASSIFIED, with the reason logged.
func (m *mutantExecutor) holdsSource(name string) bool {
	err := m.copies.reason(name)
	if err != nil {
		log.Errorf("the copy of %s does not hold the source, so the mutant at %s runs no test\n\t%v\n", name, m.mutant.Position(), err)
	}

	return err == nil
}

// runTests runs the mutant's tests: the subset the selection chooses, its verdict confirmed where
// a subset can differ from the whole suite (confirm), or else every test of the package.
func (m *mutantExecutor) runTests(rootDir, pkg string) outcome {
	// The selection waits for the package's index before the mutant's time starts.
	pattern := m.selectedTests(pkg)
	o := m.runSuite(rootDir, pkg, pattern)
	if pattern == "" {
		return o
	}

	return m.confirm(rootDir, pkg, pattern, o)
}

// confirm is the verdict of a subset run that ended o, once checked against the whole suite where
// the two can differ. A survivor runs every test, since a test that kills it may have been left out
// or need the tests before it. A kill, a time-out or a crash runs every test when the subset fails
// on the unmutated code too. A kill runs every test when it is in the sample (Sampled): a pass
// makes the package's later mutants run every test, and a run that neither passes nor fails is
// confirmed again by Settle, as is any other kill. A build failure, or a run that could not be
// carried out, is its own verdict; a time-out or a crash runs again alone, with every test.
func (m *mutantExecutor) confirm(rootDir, pkg, pattern string, o outcome) outcome {
	switch {
	case o == passed:
		return m.runSuite(rootDir, pkg, "")
	case o == unbuilt || o == broken:
		return o
	case !m.selector.Passes(pkg, pattern):
		return m.runSuite(rootDir, pkg, "")
	case o != failed:
		return o
	}
	id := mutantID(m.mutant)
	if !m.selector.Sampled(pkg, id) {
		m.unsettled.kill(m.mutant, subsetKill{pkg: pkg})

		return o
	}
	full := m.runSuite(rootDir, pkg, "")
	if full == passed {
		m.selector.Disable(pkg, fmt.Errorf("the tests %s killed the mutant %s, every test let it live", pattern, id))

		return passed
	}
	m.unsettled.kill(m.mutant, subsetKill{pkg: pkg, confirmed: full == failed, recheck: full != failed})

	return failed
}

// runSuite runs the tests of pkg that pattern selects (every test for "") on the mutant, within
// the mutant's time, and reads how the run ended from its exit code and go test's -json events.
func (m *mutantExecutor) runSuite(rootDir, pkg, pattern string) outcome {
	ctx, cancel := context.WithTimeout(context.Background(), m.testExecutionTime)
	defer cancel()

	cmd := m.execContext(ctx, "go", m.getTestArgs(pkg, pattern)...)
	cmd.Dir = m.mutant.Workdir()
	if m.integrationMode {
		cmd.Dir = rootDir
	}
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOTMPDIR=%s", m.wdDealer.WorkDir()))
	pos := m.mutant.Position()
	ev := &events{pkg: pkg, dir: cmd.Dir, file: filepath.Join(m.mutant.Workdir(), pos.Filename), first: firstLine(m.mutant), line: pos.Line}
	cmd.Stdout = ev
	killTree(cmd)
	cmd.WaitDelay = waitDelay

	rel, err := run(cmd)
	defer rel()

	return classify(errors.Is(ctx.Err(), context.DeadlineExceeded), exitCode(err), ev)
}

// selectedTests is the -run pattern of the tests of pkg that cover the mutant's position, or ""
// for every test: without a selector, in integration mode, or when no single test covers it.
func (m *mutantExecutor) selectedTests(pkg string) string {
	if m.selector == nil || m.integrationMode {
		return ""
	}

	return m.selector.Pattern(pkg, m.mutant.Position())
}

func (m *mutantExecutor) getTestArgs(pkg, pattern string) []string {
	// Vet refuses some mutants that build before any test runs on them: INVERT_LOGICAL turns
	// `x == a || x == b` into `x == a && x == b`, which vet reports as a suspect and.
	args := []string{"test", "-json", "-vet=off"}
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
	if pattern != "" {
		args = append(args, "-run", pattern)
	}

	path := pkg
	if m.integrationMode {
		path = "./..."
	}
	args = append(args, path)

	return args
}

func run(cmd *exec.Cmd) (func(), error) {
	if err := runTree(cmd); err != nil {

		return func() {}, err
	}

	return func() {
		err := cmd.Process.Release()
		if err != nil {
			_ = cmd.Process.Kill()
		}
	}, nil
}
