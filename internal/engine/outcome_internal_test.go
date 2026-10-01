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
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// classifyCases are how go test runs end and the outcome each is read as.
var classifyCases = []struct {
	name   string
	late   bool
	code   int
	output string
	want   outcome
}{
	{"past its time", true, 1, "--- FAIL: TestA\n", timedOut},
	{"every test passed", false, 0, "ok  \texample.com/p\t0.1s\n", passed},
	{"a command that did not run", false, noExit, "", broken},
	{"a go command a signal ended", false, -1, "", crashed},
	{"a build failure", false, 1, "# example.com/p\n./p.go:3:9: undefined: x\nFAIL\texample.com/p [build failed]\nFAIL\n", unbuilt},
	{"a setup failure", false, 1, "FAIL\texample.com/p [setup failed]\n", unbuilt},
	{"a test binary past its -timeout", false, 1, "panic: test timed out after 3s\n\trunning tests:\nFAIL\texample.com/p\t3.0s\n", timedOut},
	{"a test binary a signal ended", false, 1, "signal: killed\nFAIL\texample.com/p\t2.0s\n", crashed},
	{"a failed test", false, 1, "--- FAIL: TestA (0.00s)\nFAIL\nFAIL\texample.com/p\t0.1s\n", failed},
	{"a panic in a test", false, 1, "--- FAIL: TestA (0.00s)\npanic: runtime error: index out of range [3] with length 3\nFAIL\texample.com/p\t0.1s\n", failed},
	{"a test that logs a signal", false, 1, "    p_test.go:9: signal: killed\n--- FAIL: TestA (0.00s)\nFAIL\n", failed},
	{"a package go test could not test", false, 2, "", unbuilt},
	{"any other exit", false, 3, "", broken},
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyCases {
		if got := classify(tc.late, tc.code, []byte(tc.output)); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
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
		{broken, mutator.Unclassified, true},
	}
	for _, tc := range cases {
		if tc.o.status() != tc.status || tc.o.again() != tc.again {
			t.Errorf("%d: %v, %t; want %v, %t", tc.o, tc.o.status(), tc.o.again(), tc.status, tc.again)
		}
	}
}

// aloneCases are a first outcome that needs a run alone, the run alone's outcome, and the verdict.
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
	{crashed, passed, mutator.Lived},
	{crashed, failed, mutator.Killed},
	{broken, broken, mutator.Unclassified},
	{broken, timedOut, mutator.Unclassified},
	{broken, crashed, mutator.Unclassified},
	{broken, passed, mutator.Lived},
	{broken, failed, mutator.Killed},
}

func TestATimeOutOrACrashCountsOnlyWhenItRepeatsAlone(t *testing.T) {
	for _, tc := range aloneCases {
		if got := aloneStatus(tc.first, tc.alone); got != tc.want {
			t.Errorf("%d then %d alone: %v, want %v", tc.first, tc.alone, got, tc.want)
		}
	}
}

func TestTheOutputsEndIsKept(t *testing.T) {
	var out tail
	head := bytes.Repeat([]byte("h"), maxOutput)
	end := []byte("FAIL\texample.com/p [build failed]\n")
	for _, p := range [][]byte{head, end} {
		if n, err := out.Write(p); n != len(p) || err != nil {
			t.Fatalf("wrote %d of %d: %v", n, len(p), err)
		}
	}
	if len(out.buf) != maxOutput || !bytes.HasSuffix(out.buf, end) {
		t.Fatalf("kept %d bytes, ending %q", len(out.buf), out.buf[max(len(out.buf)-40, 0):])
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
