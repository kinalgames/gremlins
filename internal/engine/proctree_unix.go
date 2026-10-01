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
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// killTree makes cmd's process lead a process group of its own, and its cancellation kill the whole
// group: the test binary go test starts would outlive a killed go command.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// groups are the process groups of the runs under way, by the pid of their leader.
var groups = struct {
	sync.Mutex
	leaders map[int]struct{}
}{leaders: map[int]struct{}{}}

// runTree runs cmd, recording its process group while it runs so that Interrupt reaches it: a
// group of its own is outside the terminal's foreground group, which a Ctrl-C signals.
func runTree(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	groups.Lock()
	groups.leaders[pid] = struct{}{}
	groups.Unlock()
	defer func() {
		groups.Lock()
		delete(groups.leaders, pid)
		groups.Unlock()
	}()

	return cmd.Wait()
}

// Interrupt sends sig, an interrupt or a termination Gremlins received, to the process group of
// every run under way.
func Interrupt(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	groups.Lock()
	defer groups.Unlock()
	for pid := range groups.leaders {
		_ = syscall.Kill(-pid, s)
	}
}
