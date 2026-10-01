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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-gremlins/gremlins/internal/mutator"
)

// alive reports whether the process pid runs, as tasklist lists it.
func alive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output() //nolint:gosec // a pid this test started

	return err == nil && strings.Contains(string(out), " "+strconv.Itoa(pid)+" ")
}

func TestWithoutTaskkillATimedOutRunStillEnds(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	spawn := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := getCmd(ctx, []string{"-test.run=^TestProcessSpawns$"})
		cmd.Env = append(cmd.Env, "GREMLINS_PIDFILE="+pidFile)

		return cmd
	}
	t.Setenv("PATH", "") // taskkill cannot be found: the go command alone is killed
	mut := mutantAt(mutator.Runnable, 3)
	runMutant(t, mut, &commands{subset: spawn, whole: spawn}, time.Second)
	if pid, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // a file of this test's temporary directory
		n, _ := strconv.Atoi(string(pid))
		if p, err := os.FindProcess(n); err == nil {
			_ = p.Kill()
		}
	}
	if mut.Status() != mutator.TimedOut {
		t.Fatalf("%v", mut.Status())
	}
}
