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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

func TestReadToolchainIsWhatTheGoCommandAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	t.Setenv("GOFLAGS", "-count=1")
	got, err := engine.ReadToolchain(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.GoVersion, "go1.") || got.GOOS != runtime.GOOS || got.GOARCH != runtime.GOARCH || got.GOFLAGS != "-count=1" {
		t.Errorf("%+v", got)
	}
}

func TestReadToolchainFailsWhereTheGoCommandCannotRun(t *testing.T) {
	if _, err := engine.ReadToolchain(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil || !strings.HasPrefix(err.Error(), "go env: ") {
		t.Fatalf("%v, want the go command's failure", err)
	}
}

// pendingIn defers, on a dealer of the module root, a mutant of its file p/a.go.
func pendingIn(t *testing.T, root string) (engine.Deferred, error) {
	t.Helper()
	dealer, _ := aloneDealerIn(t, root, &sequence{}, "")
	mut := mutantAt(mutator.Runnable, 8)
	mut.position.Filename = fileP

	return dealer.Defer([]mutator.Mutator{mut})
}

// writtenFiles are the files a module root holds beside treeFiles: p/other.json, p/pending.json,
// a temporary file a write left beside it, and p/report.json, in the order Defer records them.
var writtenFiles = []string{"p/other.json", "p/pending.json", "p/pending.json.42", "p/report.json"}

// writtenCases are where --settle-later and -o point, relative to that module root, and which of
// writtenFiles Defer records.
var writtenCases = []struct {
	name, later, output string
	relative            bool
	recorded            []string
}{
	{"both in the package", writtenFiles[1], writtenFiles[3], false, writtenFiles[:1]},
	{"neither set", "", "", false, writtenFiles},
	{"relative paths", writtenFiles[1], writtenFiles[3], true, writtenFiles},
	{"outside the module", "../pending.json", "../p/report.json", false, writtenFiles},
}

func TestDeferLeavesOutTheFilesTheRunsWrite(t *testing.T) {
	for _, tc := range writtenCases {
		t.Run(tc.name, func(t *testing.T) {
			root := treeRoot(t)
			for _, name := range writtenFiles {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			at := func(p string) string {
				if p == "" || tc.relative {
					return p
				}

				return filepath.Join(root, filepath.FromSlash(p))
			}
			viperSet(map[string]any{configuration.UnleashSettleLaterKey: at(tc.later), configuration.UnleashOutputKey: at(tc.output)})
			got, err := pendingIn(t, root)
			viperReset()
			if err != nil {
				t.Fatal(err)
			}
			var recorded []string
			for _, f := range got.Files {
				if strings.HasSuffix(f.Path, ".json") || strings.HasSuffix(f.Path, ".42") {
					recorded = append(recorded, f.Path)
				}
			}
			if !slices.Equal(recorded, tc.recorded) || got.Output != at(tc.output) {
				t.Errorf("recorded %v and the report %q, want %v and %q", recorded, got.Output, tc.recorded, at(tc.output))
			}
		})
	}
}

func TestDeferLeavesOutWhatIsNotARegularFile(t *testing.T) {
	root := treeRoot(t)
	if err := os.Symlink(filepath.Join(root, "q"), filepath.Join(root, "p", "linked")); err != nil {
		t.Skipf("no symbolic link here: %v", err)
	}
	got, err := pendingIn(t, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Files {
		if strings.HasPrefix(f.Path, "p/linked") {
			t.Errorf("recorded %s", f.Path)
		}
	}
}

func TestDeferFailsOnAFileOfThePackageItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test's user")
	}
	root := treeRoot(t)
	if err := os.Chmod(filepath.Join(root, "p", "testdata", "in.txt"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := pendingIn(t, root); err == nil {
		t.Fatal("deferred without the digest of an unreadable file")
	}
}
