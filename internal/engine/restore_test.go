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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

const source = "package p\n"

// renewal is how a dealer's Renew answers: the content of a.go in the fresh copy (none when
// missing), or an error; noSource removes a.go from the source before.
type renewal struct {
	content  string
	err      error
	missing  bool
	noSource bool
}

// restoreDealer is a dealer running seq's commands on a module whose a.go holds source; renew
// answers each Renew, and renewed collects the names it was asked for.
func restoreDealer(t *testing.T, seq *sequence, renew renewal) (*engine.MutantExecutorDealer, *[]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, fileA), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var renewed []string
	wdd := &dealerStub{t: t, fnGet: func(string) (string, error) { return t.TempDir(), nil }, fnRenew: func(name string) (string, error) {
		renewed = append(renewed, name)
		if renew.err != nil {
			return "", renew.err
		}
		dir := t.TempDir()
		if renew.noSource {
			_ = os.Remove(filepath.Join(root, fileA))
		}
		if renew.missing {
			return dir, nil
		}

		return dir, os.WriteFile(filepath.Join(dir, fileA), []byte(renew.content), 0o600)
	}}
	mod := gomodule.GoModule{Name: expectedModule, Root: root, CallingDir: "."}

	return engine.NewExecutorDealer(mod, tempDealer{wdd}, aloneElapsed, engine.WithExecContext(seq.exec)), &renewed
}

// restoreCases are how the copy a mutant could not be restored in is renewed, and the verdict of
// the next mutant of the same worker.
var restoreCases = []struct {
	name  string
	renew renewal
	next  mutator.Status
	runs  int
}{
	{"a fresh copy that holds the source", renewal{content: source}, mutator.Killed, 2},
	{"a fresh copy that does not", renewal{content: "package q\n"}, mutator.Unclassified, 1},
	{"no fresh copy", renewal{err: errors.New("disk full")}, mutator.Unclassified, 1},
	{"a fresh copy without the file", renewal{missing: true}, mutator.Unclassified, 1},
	{"a source that cannot be read", renewal{content: source, noSource: true}, mutator.Unclassified, 1},
}

func TestACopyAMutantCannotBeRestoredInIsRenewed(t *testing.T) {
	for _, tc := range restoreCases {
		t.Run(tc.name, func(t *testing.T) {
			seq := &sequence{runs: []execContext{fakeExecCommandTestsFailure}}
			dealer, renewed := restoreDealer(t, seq, tc.renew)
			stuck := mutantAt(mutator.Runnable, 3)
			stuck.hasRollbackError = true
			start(dealer, stuck, "w")
			next := mutantAt(mutator.Runnable, 4)
			start(dealer, next, "w")
			other := mutantAt(mutator.Runnable, 5)
			start(dealer, other, "v")
			if stuck.Status() != mutator.Killed || strings.Join(*renewed, ",") != "w-1" {
				t.Fatalf("%v, renewed %v", stuck.Status(), *renewed)
			}
			if next.Status() != tc.next || next.applyCalled != (tc.next == mutator.Killed) || other.Status() != mutator.Killed || len(seq.all()) != tc.runs+1 {
				t.Errorf("next %v, applied %t, other worker %v after %v; want %v after %d", next.Status(), next.applyCalled, other.Status(), seq.all(), tc.next, tc.runs+1)
			}
		})
	}
}

func TestAMutantRunsNoTestAloneWhereTheCopyDoesNotHoldTheSource(t *testing.T) {
	seq := &sequence{runs: []execContext{crashedBinary}}
	dealer, _ := restoreDealer(t, seq, renewal{err: errors.New("disk full")})
	first := mutantAt(mutator.Runnable, 3)
	first.hasRollbackError = true // the run alone cannot be restored, and its copy not renewed
	second := mutantAt(mutator.Runnable, 4)
	start(dealer, first, "w")
	start(dealer, second, "v")
	dealer.Settle(context.Background(), []mutator.Mutator{first, second})
	if first.Status() != mutator.Unclassified || second.Status() != mutator.Unclassified || len(seq.all()) != 3 {
		t.Fatalf("%v, %v after %v; want both UNCLASSIFIED after 3 runs: no unmutated run, no second run alone", first.Status(), second.Status(), seq.all())
	}
}

func TestAnApplyOrRestoreFailureEndsItsLogLine(t *testing.T) {
	errOut := &bytes.Buffer{}
	log.Reset()
	log.Init(&bytes.Buffer{}, errOut)
	defer log.Reset()
	dealer, _ := restoreDealer(t, &sequence{runs: []execContext{fakeExecCommandSuccess}}, renewal{content: source})
	unapplied := mutantAt(mutator.Runnable, 3)
	unapplied.hasApplyError = true
	start(dealer, unapplied, "w")
	unrestored := mutantAt(mutator.Runnable, 4)
	unrestored.hasRollbackError = true
	start(dealer, unrestored, "w")
	lines := strings.Split(errOut.String(), "\n")
	if len(lines) != 5 || lines[4] != "" || !strings.Contains(lines[0], "failed to apply") || !strings.Contains(lines[2], "failed to restore") {
		t.Fatalf("logged %q", errOut.String())
	}
}
