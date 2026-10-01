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
	"strings"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// outcome is how one go test run on a mutant ended.
type outcome int

const (
	passed   outcome = iota // every test it ran passed
	failed                  // a test failed on the mutant: a panic inside a test is one
	unbuilt                 // the mutated code does not build
	timedOut                // the run passed its time, or the test binary its -timeout
	crashed                 // the package failed with no test failing: a signal, a panic outside a test, TestMain's exit
	broken                  // the run could not be carried out: the go command did not start, or refused its flags
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

// status is the verdict of a mutant whose run ended so.
func (o outcome) status() mutator.Status {
	return statuses[o]
}

// again reports whether a mutant whose run ended so runs again alone, once every other mutant has
// run: the load it ran under may have caused the outcome.
func (o outcome) again() bool {
	return o == timedOut || o == crashed
}

// aloneStatus is the verdict of a mutant whose run ended first and whose run alone, with every test
// of its package, ended alone. A time-out or a crash counts as a kill only when it repeats; a
// pass, a test failure or a build failure is its own verdict; anything else is unclassified.
func aloneStatus(first, alone outcome) mutator.Status {
	switch {
	case alone == timedOut && first == timedOut:
		return mutator.TimedOut
	case alone == crashed && first == crashed:
		return mutator.Killed
	case alone.again():
		return mutator.Unclassified
	default:
		return alone.status()
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
// that the run's time had passed. Exit status 2 is the go command refusing its flags.
func classify(late bool, code int, ev *events) outcome {
	switch {
	case late:
		return timedOut
	case code == 0:
		return passed
	case code == noExit || code > 1:
		return broken
	case code < 0:
		return crashed
	case ev.unbuilt:
		return unbuilt
	case ev.testFailed:
		return failed
	case ev.timedOut:
		return timedOut
	default:
		return crashed
	}
}

// maxEvent bounds an event line of go test -json: a longer one, an output line of that size, is
// skipped, which no event classify reads can be.
const maxEvent = 1 << 20

// timeoutPanic starts the output line of a test binary past its -timeout.
const timeoutPanic = "panic: test timed out after "

// events reads the events go test -json writes, line by line as they come, and keeps what classify
// needs: a build that failed, a test that failed, a test binary past its -timeout.
type events struct {
	line       []byte
	skip       bool // the line being read passed maxEvent
	unbuilt    bool
	testFailed bool
	timedOut   bool
}

// event is the part of a test2json event that classify reads.
type event struct {
	Action      string
	Test        string
	FailedBuild string
	Output      string
}

func (e *events) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			e.add(p)

			break
		}
		e.add(p[:end])
		e.read()
		p = p[end+1:]
	}

	return n, nil
}

// add appends p to the line being read, or skips the line once it passes maxEvent.
func (e *events) add(p []byte) {
	if e.skip || len(e.line)+len(p) > maxEvent {
		e.skip, e.line = true, e.line[:0]

		return
	}
	e.line = append(e.line, p...)
}

// read reads the complete line as an event; a line that is not one, such as a line of the go
// command's own, says nothing.
func (e *events) read() {
	var ev event
	if !e.skip && json.Unmarshal(e.line, &ev) == nil {
		e.unbuilt = e.unbuilt || ev.Action == "build-fail" || (ev.Action == "fail" && ev.FailedBuild != "")
		e.testFailed = e.testFailed || (ev.Action == "fail" && ev.Test != "")
		e.timedOut = e.timedOut || (ev.Action == "output" && strings.HasPrefix(ev.Output, timeoutPanic))
	}
	e.skip, e.line = false, e.line[:0]
}
