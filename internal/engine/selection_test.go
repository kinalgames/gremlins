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
	"go/token"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/engine/workerpool"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// indexStub answers one index for every package and counts its calls.
type indexStub struct {
	m     sync.Mutex
	calls int
}

func (s *indexStub) Index(string) (selection.Index, error) {
	s.m.Lock()
	defer s.m.Unlock()
	s.calls++

	return selection.Index{
		Tests: []string{"TestOne", "TestTwo"},
		Profiles: []coverage.Profile{
			{"a.go": {{StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 2}}},
			{"a.go": {{StartLine: 4, StartCol: 1, EndLine: 9, EndCol: 2}}},
		},
	}, nil
}

// commands records every command a dealer starts and runs each as a success.
type commands struct {
	m    sync.Mutex
	runs [][]string
}

func (c *commands) exec(ctx context.Context, command string, args ...string) *exec.Cmd {
	c.m.Lock()
	defer c.m.Unlock()
	c.runs = append(c.runs, append([]string{command}, args...))

	return fakeExecCommandSuccess(ctx, command, args...)
}

// runMutant runs mut with the dealer's executor and answers the commands it started.
func runMutant(t *testing.T, mut *mutantStub, opts ...engine.ExecutorDealerOption) [][]string {
	t.Helper()
	got := &commands{}
	mod := gomodule.GoModule{Name: expectedModule, Root: t.TempDir(), CallingDir: "."}
	dealer := engine.NewExecutorDealer(mod, tempDealer{newWdDealerStub(t)}, expectedTimeout,
		append([]engine.ExecutorDealerOption{engine.WithExecContext(got.exec)}, opts...)...)
	outCh := make(chan mutator.Mutator)
	wg := sync.WaitGroup{}
	wg.Add(1)
	executor := dealer.NewExecutor(mut, outCh, &wg)
	go func() {
		<-outCh
		close(outCh)
	}()
	executor.Start(&workerpool.Worker{Name: "selection", ID: 1})
	wg.Wait()

	return got.runs
}

// tempDealer is a work directory dealer whose work directory exists.
type tempDealer struct{ *dealerStub }

func (d tempDealer) WorkDir() string { return d.t.TempDir() }

func tokenPosition(file string, line int) token.Position {
	return token.Position{Filename: file, Line: line, Column: 2}
}

func mutantAt(status mutator.Status, line int) *mutantStub {
	return &mutantStub{status: status, mutType: mutator.ConditionalsBoundary, pkg: "example.com/p",
		position: tokenPosition("a.go", line)}
}

func TestAMutantRunsTheTestsThatCoverIt(t *testing.T) {
	cases := []struct {
		line int
		run  []string
	}{
		{3, []string{"-run", "^(TestOne)$"}},
		{4, []string{"-run", "^(TestOne|TestTwo)$"}},
		{12, nil},
	}
	for _, tc := range cases {
		stub := &indexStub{}
		runs := runMutant(t, mutantAt(mutator.Runnable, tc.line), engine.WithSelector(selection.New(stub, func(string, error) {})))
		if len(runs) != 1 {
			t.Fatalf("line %d: commands %v", tc.line, runs)
		}
		args := runs[0]
		i := slices.Index(args, "-run")
		if (tc.run == nil) != (i < 0) || (i >= 0 && (args[i+1] != tc.run[1] || args[len(args)-1] != "example.com/p")) {
			t.Errorf("line %d: %v, want %v before the package", tc.line, args, tc.run)
		}
	}
}

func TestAMutantNoTestCoversIsNeitherIndexedNorRun(t *testing.T) {
	stub := &indexStub{}
	mut := mutantAt(mutator.NotCovered, 4)
	if runs := runMutant(t, mut, engine.WithSelector(selection.New(stub, func(string, error) {}))); len(runs) != 0 || stub.calls != 0 {
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
	runs := runMutant(t, mutantAt(mutator.Runnable, 4), engine.WithSelector(selection.New(stub, func(string, error) {})))
	if len(runs) != 1 || slices.Contains(runs[0], "-run") || stub.calls != 0 {
		t.Fatalf("commands %v, %d indexings", runs, stub.calls)
	}
}

func TestTheTestSelectionSettingIndexesWithTheGoCommand(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]any
		indexed  bool
	}{
		{"on, two workers", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashWorkersKey: 2}, true},
		{"on, the default workers", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashWorkersKey: 0}, true},
		{"on, in integration mode", map[string]any{configuration.UnleashTestSelectionKey: true, configuration.UnleashIntegrationMode: true}, false},
		{"off", map[string]any{configuration.UnleashTestSelectionKey: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viperSet(tc.settings)
			defer viperReset()
			runs := runMutant(t, mutantAt(mutator.Runnable, 4))
			built := len(runs) == 2 && strings.HasPrefix(strings.Join(runs[0], " "), "go test -c -cover -covermode=set -o ")
			if built != tc.indexed || len(runs) == 0 || slices.Contains(runs[len(runs)-1], "-run") {
				t.Fatalf("commands %v: want the test binary built %t, then every test of the mutant's package run", runs, tc.indexed)
			}
		})
	}
}
