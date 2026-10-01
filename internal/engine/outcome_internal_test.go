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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	failedBuild  = `{"Action":"fail","Package":"example.com/p","Elapsed":0,"FailedBuild":"example.com/p [example.com/p.test]"}` + "\n"
	setupFail    = `{"Action":"fail","Package":"example.com/p","Elapsed":0,"FailedBuild":"example.com/nope"}` + "\n"
	panicInTest  = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"panic: runtime error: index out of range [3] with length 0 [recovered, repanicked]\n"}` + "\n"
	binaryLate   = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"panic: test timed out after 2s\n"}` + "\n"
	killedBinary = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"signal: killed\n"}` + "\n"
	initPanic    = `{"Action":"output","Package":"example.com/p","Output":"panic: init\n"}` + "\n"
	mainExit     = `{"Action":"output","Package":"example.com/p","Output":"exit status 3\n"}` + "\n"
	buildHeader  = `{"ImportPath":"example.com/p [example.com/p.test]","Action":"build-output","Output":"# example.com/p [example.com/p.test]\n"}` + "\n"
	lookalike    = `{"Action":"output","Package":"example.com/p","Test":"TestA","Output":"FAIL\texample.com/p [build failed]\n"}` + "\n"
)

// mutatedAt is the go command's directory of the cases, whose p.go is mutated at line 6.
var mutatedAt = filepath.Join(os.TempDir(), "p")

// compiled is the compiler's message about file and line, from the package importPath.
func compiled(importPath, file, line string) string {
	output, _ := json.Marshal(file + ":" + line + ":12: invalid operation: x + \"a\" (mismatched types int and untyped string)\n")

	return `{"ImportPath":"` + importPath + `","Action":"build-output","Output":` + string(output) + "}\n"
}

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
	{"a go command a signal ended", false, -1, "", broken},
	{"a go command a signal ended after the tests failed", false, -1, packageFail, crashed},
	{"the mutated line does not build", false, 1, compiled("example.com/p [example.com/p.test]", "./p.go", "6") + buildFail + failedBuild, unbuilt},
	{"the mutated line of a package without internal tests does not build", false, 1, compiled("example.com/p", "./p.go", "6") + buildFail + failedBuild, unbuilt},
	{"the mutated line, named by its absolute path, does not build", false, 1, compiled("example.com/p", filepath.Join(mutatedAt, "p.go"), "6") + buildFail + failedBuild, unbuilt},
	{"another line does not build", false, 1, compiled("example.com/p [example.com/p.test]", "./p.go", "16") + buildFail + failedBuild, broken},
	{"another file does not build", false, 1, compiled("example.com/p [example.com/p.test]", "./q.go", "6") + buildFail + failedBuild, broken},
	{"the same file of another directory does not build", false, 1, compiled("example.com/p [example.com/p.test]", "../q/p.go", "6") + buildFail + failedBuild, broken},
	{"the external test package does not build", false, 1, compiled("example.com/p_test [example.com/p.test]", "./p.go", "6") + buildFail + failedBuild, broken},
	{"another package does not build", false, 1, compiled("example.com/q", "./p.go", "6") + buildFail + failedBuild, broken},
	{"only the package's header", false, 1, buildHeader + buildFail + failedBuild, broken},
	{"a setup failure", false, 1, setupFail, broken},
	{"a failed test", false, 1, testFail + packageFail, failed},
	{"a panic in a test", false, 1, panicInTest + testFail + packageFail, failed},
	{"a test binary past its -timeout", false, 1, binaryLate + packageFail, crashed},
	{"a test binary a signal ended", false, 1, killedBinary + packageFail, crashed},
	{"a panic outside a test", false, 1, initPanic + packageFail, crashed},
	{"TestMain's exit", false, 1, mainExit + packageFail, crashed},
	{"a test that prints a build failure", false, 1, lookalike + packageFail, crashed},
	{"the go command's own line", false, 1, "go: downloading example.com/q v1.0.0\n", broken},
}

func eventsOf(lines string) *events {
	ev := &events{pkg: "example.com/p", dir: mutatedAt, file: filepath.Join(mutatedAt, "p.go"), line: 6}
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
	ev := eventsOf("")
	for _, b := range stream {
		if n, err := ev.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("wrote %d: %v", n, err)
		}
	}
	if !ev.testFailed || !ev.packageFailed || ev.buildFailed {
		t.Fatalf("read %+v", *ev)
	}
	if ev = eventsOf(strings.TrimSuffix(testFail, "\n")); ev.testFailed {
		t.Fatal("read a line before its end")
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
	{timedOut, unbuilt, mutator.Unclassified}, // the first run built the same code
	{crashed, crashed, mutator.Killed},
	{crashed, timedOut, mutator.Unclassified},
	{crashed, broken, mutator.Unclassified},
	{crashed, unbuilt, mutator.Unclassified},
	{crashed, passed, mutator.Lived},
	{crashed, failed, mutator.Killed},
	{failed, failed, mutator.Killed}, // a subset kill the whole suite confirms
	{failed, passed, mutator.Lived},
	{failed, timedOut, mutator.Unclassified},
	{failed, crashed, mutator.Unclassified},
	{failed, broken, mutator.Unclassified},
	{failed, unbuilt, mutator.Unclassified},
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
