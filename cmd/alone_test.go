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

package cmd

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// envfaultCases are whether the shared service of cmd/testdata/envfault is down when a run settles
// the two mutants another run deferred, and their verdicts: B's kill alone counts only while every
// test passes on the unmutated code.
var envfaultCases = []struct {
	name string
	down bool
	want map[string]mutator.Status
}{
	{"the service up", false, map[string]mutator.Status{"envfault.go:7 ARITHMETIC_BASE": mutator.Lived, "envfault.go:10 ARITHMETIC_BASE": mutator.Killed}},
	{"the service down", true, map[string]mutator.Status{"envfault.go:7 ARITHMETIC_BASE": mutator.Unclassified, "envfault.go:10 ARITHMETIC_BASE": mutator.Unclassified}},
}

func TestAKillAloneCountsOnlyWhereEveryTestPassesOnTheUnmutatedCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs gremlins with the go command")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", modMod)
	dir := filepath.Join("testdata", "envfault")
	for _, tc := range envfaultCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENVFAULT_MARKER", filepath.Join(t.TempDir(), "crashed"))
			down := filepath.Join(t.TempDir(), "down")
			t.Setenv("ENVFAULT_DOWN", down)
			path := filepath.Join(t.TempDir(), "deferred.json")
			if first, logged := unleash(t, dir, "", map[string]any{configuration.UnleashSettleLaterKey: path, configuration.UnleashTimeoutCoefficientKey: 30}, nil); len(first) != 0 {
				t.Fatalf("the deferring run reported %v; logged %s", first, logged)
			}
			if tc.down {
				if err := os.WriteFile(down, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, logged := unleash(t, dir, "", map[string]any{configuration.UnleashSettleKey: path}, nil)
			if !maps.Equal(got, tc.want) || strings.Contains(logged, "failed a test alone, and every test failed a test on the unmutated code there") != tc.down {
				t.Errorf("settled %v, want %v; logged %s", got, tc.want, logged)
			}
		})
	}
}
