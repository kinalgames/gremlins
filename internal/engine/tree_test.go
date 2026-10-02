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

// writeFiles writes each of files, by its path from root, with its content.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Where the runs of the provenance tests write, from the module root: the deferring run's settle
// file and report, and the settling run's report.
const (
	pendingFile = "p/pending.json"
	reportFile  = "p/report.json"
	settledFile = "p/settled.json"
	// fixtureContent is a file's content when a run defers its mutants.
	fixtureContent = "fixture"
)

func TestDeferRecordsTheFilesAtThePathsTheRunsWrite(t *testing.T) {
	root := treeRoot(t)
	ownFiles := []string{pendingFile, pendingFile + ".42", reportFile}
	for _, name := range ownFiles {
		writeFiles(t, root, map[string]string{name: name})
	}
	report := filepath.Join(root, filepath.FromSlash(reportFile))
	viperSet(map[string]any{configuration.UnleashSettleLaterKey: filepath.Join(root, filepath.FromSlash(pendingFile)), configuration.UnleashOutputKey: report})
	got, err := pendingIn(t, root)
	viperReset()
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, f := range got.Files {
		if slices.Contains(ownFiles, f.Path) {
			recorded = append(recorded, f.Path)
		}
	}
	if !slices.Equal(recorded, ownFiles) || got.Output != report {
		t.Errorf("recorded %v and the report %q, want %v and %q", recorded, got.Output, ownFiles, report)
	}
}

// provenanceCases are files of a module root of treeFiles: those it holds when a run defers a
// mutant of p with --settle-later at pendingFile and -o at reportFile, and those it holds when a
// run settles it with --settle at settle (from the root; ".." leaves the module) and -o at output
// ("" for none); and the start of the settling run's error, "" for none.
var provenanceCases = []struct {
	name           string
	before, after  map[string]string
	settle, output string
	want           string
}{
	{"the files the runs wrote since the run deferred", nil,
		map[string]string{pendingFile: "{}", pendingFile + ".42": "", reportFile: "{}", settledFile: "{}"},
		pendingFile, settledFile, ""},
	{"a file beside the settle file", nil, map[string]string{pendingFile + ".bak": "x"}, pendingFile, "", pendingFile + ".bak is new since"},
	{"a directory named after the settle file", nil, map[string]string{pendingFile + ".d/x.go": "package d"}, pendingFile, "", pendingFile + ".d/x.go is new since"},
	{"a settle file outside the module", nil, map[string]string{pendingFile: "{}"}, "../" + pendingFile, "", pendingFile + " is new since"},
	{"the settle file, recorded", map[string]string{pendingFile: fixtureContent}, map[string]string{pendingFile: "{}"}, pendingFile, "", pendingFile + " changed since"},
	{"a temporary file's name, recorded", map[string]string{pendingFile + ".42": fixtureContent}, map[string]string{pendingFile + ".42": ""}, pendingFile, "", pendingFile + ".42 changed since"},
	{"the deferring run's report, recorded", map[string]string{reportFile: fixtureContent}, map[string]string{reportFile: "{}"}, pendingFile, "", reportFile + " changed since"},
	{"the settling run's report, recorded", map[string]string{settledFile: fixtureContent}, map[string]string{settledFile: "{}"}, pendingFile, settledFile, settledFile + " changed since"},
	{"the settling run's report, recorded and unchanged", map[string]string{settledFile: fixtureContent}, nil, pendingFile, settledFile, ""},
}

func TestResumeLeavesOutOnlyTheFilesTheRunsWroteSinceTheRunDeferred(t *testing.T) {
	for _, tc := range provenanceCases {
		t.Run(tc.name, func(t *testing.T) {
			root := treeRoot(t)
			at := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
			writeFiles(t, root, tc.before)
			viperSet(map[string]any{configuration.UnleashSettleLaterKey: at(pendingFile), configuration.UnleashOutputKey: at(reportFile)})
			d, err := pendingIn(t, root)
			viperReset()
			if err != nil {
				t.Fatal(err)
			}
			d.Mutants = []engine.DeferredMutant{{File: fileP, Line: 4, Column: 11, Type: arithmeticBase, Pkg: pkgP, First: crashedRun}}
			writeFiles(t, root, tc.after)
			settings := map[string]any{configuration.UnleashSettleKey: at(tc.settle)}
			if tc.output != "" {
				settings[configuration.UnleashOutputKey] = at(tc.output)
			}
			viperSet(settings)
			defer viperReset()
			seq := &sequence{runs: []execContext{fakeExecCommandSuccess}}
			_, err = settlingIn(t, root, seq).Resume(context.Background(), d)
			if tc.want == "" && (err != nil || len(seq.all()) != 1) {
				t.Errorf("%v after %v, want the mutant run alone", err, seq.all())
			}
			if tc.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.want) || len(seq.all()) != 0) {
				t.Errorf("%v after %v, want %q before any run", err, seq.all(), tc.want)
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
