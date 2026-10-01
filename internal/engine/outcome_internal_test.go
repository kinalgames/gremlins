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
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// Events of go test -json, as go 1.27 writes them (measured on Linux and Windows).
const (
	testFail     = `{"Action":"fail","Package":"example.com/p","Test":"TestA","Elapsed":0}` + "\n"
	packageFail  = `{"Action":"fail","Package":"example.com/p","Elapsed":0.003}` + "\n"
	buildFail    = `{"Action":"build-fail","ImportPath":"example.com/p [example.com/p.test]"}` + "\n"
	setupFail    = `{"Action":"fail","Package":"example.com/p","Elapsed":0,"FailedBuild":"example.com/nope"}` + "\n"
	panicInTest  = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"panic: runtime error: index out of range [3] with length 0 [recovered, repanicked]\n"}` + "\n"
	binaryLate   = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"panic: test timed out after 2s\n"}` + "\n"
	killedBinary = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"signal: killed\n"}` + "\n"
	initPanic    = `{"Action":"output","Package":"example.com/p","Output":"panic: init\n"}` + "\n"
	mainExit     = `{"Action":"output","Package":"example.com/p","Output":"exit status 3\n"}` + "\n"
	lookalike    = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"FAIL\texample.com/p [build failed]\n"}` + "\n"
)

// classifyCases are how go test runs end and the outcome each is read as.
var classifyCases = []struct {
	name   string
	late   bool
	code   int
	events string
	want   outcome
}{
	{"past its time", true, 1, testFail + packageFail, timedOut},
	{"every test passed", false, 0, "", passed},
	{"a command that did not run", false, noExit, "", broken},
	{"a flag the go command refused", false, 2, "invalid value \"nope\" for flag -timeout: parse error\n", broken},
	{"any other exit", false, 3, "", broken},
	{"a go command a signal ended", false, -1, "", crashed},
	{"a build failure", false, 1, buildFail + packageFail, unbuilt},
	{"a setup failure", false, 1, setupFail, unbuilt},
	{"a failed test", false, 1, testFail + packageFail, failed},
	{"a panic in a test", false, 1, panicInTest + testFail + packageFail, failed},
	{"a test binary past its -timeout", false, 1, binaryLate + packageFail, timedOut},
	{"a test binary a signal ended", false, 1, killedBinary + packageFail, crashed},
	{"a panic outside a test", false, 1, initPanic + packageFail, crashed},
	{"TestMain's exit", false, 1, mainExit + packageFail, crashed},
	{"a test that prints a build failure", false, 1, lookalike + packageFail, crashed},
	{"the go command's own line", false, 1, "go: downloading example.com/q v1.0.0\n", crashed},
}

func eventsOf(lines string) *events {
	ev := &events{}
	_, _ = ev.Write([]byte(lines))

	return ev
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyCases {
		if got := classify(tc.late, tc.code, eventsOf(tc.events)); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestEventsAreReadAcrossWrites(t *testing.T) {
	stream := []byte(panicInTest + testFail + packageFail)
	ev := &events{}
	for _, b := range stream {
		if n, err := ev.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("wrote %d: %v", n, err)
		}
	}
	if !ev.testFailed || ev.unbuilt || ev.timedOut {
		t.Fatalf("read %+v", *ev)
	}
}

func TestALineLongerThanTheBoundIsSkipped(t *testing.T) {
	long := `{"Action":"fail","Package":"example.com/p","Test":"TestA","Output":"` + strings.Repeat("x", maxEvent) + `"}` + "\n"
	if ev := eventsOf(long); ev.testFailed || len(ev.line) != 0 {
		t.Fatalf("read a line of %d bytes: %+v", len(long), ev.testFailed)
	}
	if ev := eventsOf(long + buildFail); !ev.unbuilt || ev.testFailed {
		t.Fatalf("the line after a skipped one: %+v", *ev)
	}
	exact := `{"Action":"fail","Test":"TestA","Output":"`
	exact += strings.Repeat("x", maxEvent-len(exact)-2) + `"}`
	if ev := eventsOf(exact + "\n"); !ev.testFailed {
		t.Fatalf("a line of exactly %d bytes was skipped", len(exact))
	}
}

func TestEachOutcomesStatusAndWhetherItRunsAgainAlone(t *testing.T) {
	cases := []struct {
		o      outcome
		status mutator.Status
		again  bool
	}{
		{passed, mutator.Lived, false},
		{failed, mutator.Killed, false},
		{unbuilt, mutator.NotViable, false},
		{timedOut, mutator.TimedOut, true},
		{crashed, mutator.Unclassified, true},
		{broken, mutator.Unclassified, false},
	}
	for _, tc := range cases {
		if tc.o.status() != tc.status || tc.o.again() != tc.again {
			t.Errorf("%d: %v, %t; want %v, %t", tc.o, tc.o.status(), tc.o.again(), tc.status, tc.again)
		}
	}
}

// aloneCases are a first outcome that needs a run alone (a subset kill: its confirmation), the run
// alone's outcome, and the verdict.
var aloneCases = []struct {
	first, alone outcome
	want         mutator.Status
}{
	{timedOut, timedOut, mutator.TimedOut},
	{timedOut, crashed, mutator.Unclassified},
	{timedOut, broken, mutator.Unclassified},
	{timedOut, passed, mutator.Lived},
	{timedOut, failed, mutator.Killed},
	{timedOut, unbuilt, mutator.NotViable},
	{crashed, crashed, mutator.Killed},
	{crashed, timedOut, mutator.Unclassified},
	{crashed, broken, mutator.Unclassified},
	{crashed, unbuilt, mutator.NotViable},
	{crashed, passed, mutator.Lived},
	{crashed, failed, mutator.Killed},
	{failed, failed, mutator.Killed}, // a subset kill the whole suite confirms
	{failed, passed, mutator.Lived},
	{failed, timedOut, mutator.Unclassified},
	{failed, crashed, mutator.Unclassified},
	{failed, broken, mutator.Unclassified},
}

func TestATimeOutOrACrashCountsOnlyWhenItRepeatsAlone(t *testing.T) {
	for _, tc := range aloneCases {
		if got := aloneStatus(tc.first, tc.alone); got != tc.want {
			t.Errorf("%d then %d alone: %v, want %v", tc.first, tc.alone, got, tc.want)
		}
	}
}

// TestExitHelper exits with the status GREMLINS_EXIT names, or waits to be killed for "wait".
func TestExitHelper(*testing.T) {
	switch code := os.Getenv("GREMLINS_EXIT"); code {
	case "":
	case "wait":
		select {}
	default:
		n, _ := strconv.Atoi(code)
		os.Exit(n)
	}
}

func helper(code string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitHelper$") //nolint:gosec // this test binary
	cmd.Env = append(os.Environ(), "GREMLINS_EXIT="+code)

	return cmd
}

func TestTheExitCodeOfARun(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("no error: %d", got)
	}
	if got := exitCode(errors.New("exec: not started")); got != noExit {
		t.Errorf("a run that did not start: %d", got)
	}
	if got := exitCode(helper("3").Run()); got != 3 {
		t.Errorf("exit 3: %d", got)
	}
	if runtime.GOOS == "windows" {
		return // a killed process exits 1 there
	}
	cmd := helper("wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	if got := exitCode(cmd.Wait()); got != -1 {
		t.Errorf("killed: %d", got)
	}
}
