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
	"errors"
	"os/exec"
	"regexp"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// outcome is how one go test run on a mutant ended.
type outcome int

const (
	passed   outcome = iota // every test it ran passed
	failed                  // a test failed on the mutant: a panic the testing package reports is one
	unbuilt                 // the mutated code does not build
	timedOut                // the run passed its time, or the test binary its -timeout
	crashed                 // the test binary or the go command was ended by a signal
	broken                  // the run could not be carried out
)

// statuses are the verdicts of the outcomes. A time-out counts as killed; a crash or a run that
// could not be carried out reaches no verdict. Each of the three runs again alone (again), whose
// verdict is aloneStatus's.
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
	return o == timedOut || o == crashed || o == broken
}

// aloneStatus is the verdict of a mutant whose run ended first and whose run alone ended alone. A
// time-out or a crash counts as a kill only when it repeats; anything else that needs a run alone
// is unclassified; a test failure, a pass or a build failure is its own verdict.
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

var (
	// buildFailed is the line go test ends a package with when its test binary does not build.
	buildFailed = regexp.MustCompile(`(?m)^FAIL\s+\S+\s+\[(build|setup) failed\]$`)
	// testTimedOut is the panic of a test binary past its -timeout.
	testTimedOut = regexp.MustCompile(`(?m)^panic: test timed out after `)
	// signalled is the line go test prints for a test binary a signal ended.
	signalled = regexp.MustCompile(`(?m)^signal: `)
)

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

// classify is the outcome of a go test run that exited with code after printing output; late says
// that the run's time had passed.
func classify(late bool, code int, output []byte) outcome {
	switch {
	case late:
		return timedOut
	case code == 0:
		return passed
	case code == noExit:
		return broken
	case code < 0:
		return crashed
	case buildFailed.Match(output):
		return unbuilt
	case testTimedOut.Match(output):
		return timedOut
	case signalled.Match(output):
		return crashed
	case code == 1:
		return failed
	case code == 2: // go test's status for a package it could not test, kept from upstream
		return unbuilt
	default:
		return broken
	}
}

// maxOutput is how much of the end of a run's output classify reads: the lines it looks for are
// go test's last.
const maxOutput = 64 << 10

// tail keeps the last maxOutput bytes written to it.
type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - maxOutput; over > 0 {
		t.buf = t.buf[over:]
	}

	return len(p), nil
}
