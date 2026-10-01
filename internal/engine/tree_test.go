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
	"strings"
	"testing"

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
