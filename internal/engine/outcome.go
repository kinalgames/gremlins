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

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// outcome is how one go test run on a mutant ended.
type outcome int

const (
	passed   outcome = iota // every test it ran passed
	failed                  // a test failed on the mutant: a panic inside a test is one
	unbuilt                 // the compiler refused the mutated line
	timedOut                // the run passed its time
	crashed                 // the package's tests failed with no test failing: a signal, a panic outside a test, TestMain's exit
	broken                  // no verdict: the go command did not start, refused its flags, or failed before the package's tests ran
)

// statuses are the verdicts of the outcomes. A time-out counts as killed; a crash reaches no
// verdict until it runs again alone (again), as does a time-out, whose verdict is aloneStatus's;
// a run that could not be carried out reaches none.
var statuses = [...]mutator.Status{
	passed:   mutator.Lived,
	failed:   mutator.Killed,
	unbuilt:  mutator.NotViable,
	timedOut: mutator.TimedOut,
	crashed:  mutator.Unclassified,
	broken:   mutator.Unclassified,
}

// outcomeNames describe how a run ended.
var outcomeNames = [...]string{
	passed:   "passed",
	failed:   "failed a test",
	unbuilt:  "did not build",
	timedOut: "timed out",
	crashed:  "crashed",
	broken:   "reached no verdict",
}

// String describes how a run ended.
func (o outcome) String() string {
	return outcomeNames[o]
}

// status is the verdict of a mutant whose run ended so.
func (o outcome) status() mutator.Status {
	return statuses[o]
}

// again reports whether a mutant whose run ended so runs again alone, once every other mutant has
// run: the load it ran under may have caused the outcome.
func (o outcome) again() bool {
	return o == timedOut || o == crashed
}

// repeats reports whether a run alone that ended alone repeats a first run's time-out or crash,
// which then counts once the unmutated code passes in the same place (aloneStatus).
func repeats(first, alone outcome) bool {
	return alone == first && alone.again()
}

// aloneStatus is the verdict of a mutant whose run ended first and whose run alone, with every test
// of its package, ended alone. A time-out or a crash counts as a kill only when it repeats; a
// pass or a test failure is its own verdict. Anything else is unclassified, a build failure
// included: the first run built the same code.
func aloneStatus(first, alone outcome) mutator.Status {
	switch {
	case repeats(first, alone) && alone == timedOut:
		return mutator.TimedOut
	case repeats(first, alone):
		return mutator.Killed
	case alone == passed || alone == failed:
		return alone.status()
	default:
		return mutator.Unclassified
	}
}

// noExit is the exit code of a run that could not be carried out: the command did not start, or
// did not end on its own.
const noExit = -2

// exitCode is the exit code of a run that ended with err: 0 for none, -1 for a process a signal
// ended (POSIX; on Windows a killed process exits 1), noExit for a run that could not be carried
// out.
func exitCode(err error) int {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.ExitCode()
	default:
		return noExit
	}
}

// classify is the outcome of a go test -json run that exited with code and wrote ev; late says
// that the run's time had passed. Exit status 2 is the go command refusing its flags. A build
// failure is the mutant's only when the compiler names the mutated line of the mutant's own
// package; a crash, only when the package's tests ran and failed with no test failing. Any other
// failure, such as a go command that failed before running the package, is no verdict.
func classify(late bool, code int, ev *events) outcome {
	switch {
	case late:
		return timedOut
	case code == 0:
		return passed
	case code == noExit || code > 1:
		return broken
	case ev.buildFailed && ev.mutatedLine:
		return unbuilt
	case ev.buildFailed:
		return broken
	case ev.testFailed:
		return failed
	case ev.packageFailed:
		return crashed
	default:
		return broken
	}
}

// events reads the events go test -json writes for the package pkg, line by line as they come, and
// keeps what classify needs. file is the mutated file, an absolute path, and first and line the
// lines of the mutated expression, from where it starts to the mutated token; dir is the directory
// the go command runs in, which the compiler names files from.
type events struct {
	pkg, dir, file string
	first, line    int

	pending       []byte // the line being read
	buildFailed   bool   // a package failed to build
	mutatedLine   bool   // the compiler refused the mutated line of pkg
	testFailed    bool
	packageFailed bool // the package's tests failed
}

// event is the part of a test2json event that classify reads.
type event struct {
	Action      string
	ImportPath  string
	Test        string
	FailedBuild string
	Output      string
}

func (e *events) Write(p []byte) (int, error) {
	n := len(p)
	for {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			e.pending = append(e.pending, p...)

			return n, nil
		}
		e.pending = append(e.pending, p[:end]...)
		e.read()
		p = p[end+1:]
	}
}

// read reads the complete line as an event; a line that is not one says nothing.
func (e *events) read() {
	var ev event
	if json.Unmarshal(e.pending, &ev) == nil {
		failed := ev.Action == "fail"
		e.buildFailed = e.buildFailed || ev.Action == "build-fail" || (failed && ev.FailedBuild != "")
		e.mutatedLine = e.mutatedLine || (ev.Action == "build-output" && e.ownPackage(ev.ImportPath) && e.namesMutatedLine(ev.Output))
		e.testFailed = e.testFailed || (failed && ev.Test != "")
		e.packageFailed = e.packageFailed || (failed && ev.Test == "" && ev.FailedBuild == "")
	}
	e.pending = e.pending[:0]
}

// ownPackage reports whether importPath, as a build event names it, is pkg or pkg's test variant
// ("pkg [pkg.test]").
func (e *events) ownPackage(importPath string) bool {
	name, _, _ := strings.Cut(importPath, " [")

	return name == e.pkg
}

// namesMutatedLine reports whether a compiler message, "<file>:<line>:<column>: <text>" with the
// file named from the go command's directory, is about a line of the mutated expression. The
// unmutated package built, so no other error can name a line of the mutated file.
func (e *events) namesMutatedLine(message string) bool {
	name, rest, found := strings.Cut(message, ".go:")
	if !found {
		return false
	}
	digits, _, found := strings.Cut(rest, ":")
	line, err := strconv.Atoi(digits)
	if !found || err != nil || line < e.first || line > e.line {
		return false
	}
	file := name + ".go"
	if !filepath.IsAbs(file) {
		file = filepath.Join(e.dir, file)
	}

	return filepath.Clean(file) == filepath.Clean(e.file)
}

// firstLine is the line where mut's mutated expression starts, when mut knows it (TokenMutator),
// else the line of its position.
func firstLine(mut mutator.Mutator) int {
	if m, ok := mut.(interface{ FirstLine() int }); ok {
		return m.FirstLine()
	}

	return mut.Position().Line
}
