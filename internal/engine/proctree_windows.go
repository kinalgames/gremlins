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
	"strconv"
)

// killTree makes cmd's cancellation end its process and every process under it (taskkill /T): the
// test binary go test starts would outlive a killed go command, holding its work directory.
func killTree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		//nolint:gosec // the pid of a process this run started
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}

		return nil
	}
}

// runTree runs cmd. Its processes share Gremlins' console, which delivers a Ctrl-C to them too.
func runTree(cmd *exec.Cmd) error {
	return cmd.Run()
}

// Interrupt does nothing on Windows: a run's processes receive the console's Ctrl-C themselves.
func Interrupt(os.Signal) {}
