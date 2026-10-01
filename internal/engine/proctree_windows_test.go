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
	"testing"
)

func TestARunEndsWithItsExitAndAnInterruptLeavesItToTheConsole(t *testing.T) {
	Interrupt(os.Interrupt) // nothing to signal: the console reaches every process
	if got := exitCode(runTree(helper("3"))); got != 3 {
		t.Fatalf("exit %d", got)
	}
}
