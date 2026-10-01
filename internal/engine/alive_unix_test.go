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

package engine_test

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// alive reports whether the process pid runs; a zombie (Linux's /proc says Z) does not.
func alive(pid int) bool {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		fields := strings.Fields(string(stat))

		return len(fields) > 2 && fields[2] != "Z"
	}
	p, err := os.FindProcess(pid)

	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
