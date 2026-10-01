//go:build !windows

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
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// leaders is how many runs under way Interrupt reaches.
func leaders() int {
	groups.Lock()
	defer groups.Unlock()

	return len(groups.leaders)
}

func TestInterruptReachesTheProcessGroupOfEveryRunUnderWay(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestExitHelper$") //nolint:gosec // this test binary
	cmd.Env = append(os.Environ(), "GREMLINS_EXIT=wait")
	killTree(cmd)
	ended := make(chan error, 1)
	go func() { ended <- runTree(cmd) }()
	for wait := time.Now().Add(10 * time.Second); leaders() == 0; time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-ended:
			t.Fatalf("the run ended before the interrupt: %v", err)
		default:
		}
		if time.Now().After(wait) {
			t.Fatal("the run never started")
		}
	}
	Interrupt(os.Interrupt)
	select {
	case err := <-ended:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("ended with %v, want SIGINT", err)
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGINT {
			t.Fatalf("ended with %v, want SIGINT", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the run outlived the interrupt")
	}
	if leaders() != 0 {
		t.Fatalf("%d runs still recorded", leaders())
	}
}

func TestInterruptIgnoresASignalOfAnotherSystem(*testing.T) {
	Interrupt(otherSignal{}) // must not panic, nor signal anything
}

type otherSignal struct{}

func (otherSignal) String() string { return "other" }
func (otherSignal) Signal()        {}

func TestARunThatCannotStartIsNotRecorded(t *testing.T) {
	if err := runTree(exec.Command("/gremlins-no-such-command")); err == nil || leaders() != 0 {
		t.Fatalf("%v, %d recorded", err, leaders())
	}
}
