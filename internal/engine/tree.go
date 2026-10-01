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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
)

// Toolchain is the go command a run's tests ran with: its version, the platform it builds for and
// the GOFLAGS it applies. A run settles another's mutants only with the same toolchain.
type Toolchain struct {
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	GOFLAGS   string `json:"goflags,omitempty"`
}

// ReadToolchain is the Toolchain the go command answers in the directory dir.
func ReadToolchain(ctx context.Context, dir string) (Toolchain, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "GOFLAGS")
	cmd.Dir = dir
	out, err := cmd.Output()
	var env map[string]string
	if err == nil {
		err = json.Unmarshal(out, &env)
	}
	if err != nil {
		return Toolchain{}, fmt.Errorf("go env: %w", err)
	}

	return Toolchain{GoVersion: env["GOVERSION"], GOOS: env["GOOS"], GOARCH: env["GOARCH"], GOFLAGS: env["GOFLAGS"]}, nil
}

// WithToolchain records the toolchain the dealer's runs use in the settings a deferred run carries
// (Settings), so that a run settling it refuses another.
func WithToolchain(t Toolchain) ExecutorDealerOption {
	return func(m MutantExecutorDealer) MutantExecutorDealer {
		m.toolchain = t

		return m
	}
}

// moduleFiles are the module's files its packages' builds read wherever they are: go.mod and
// go.sum.
var moduleFiles = []string{"go.mod", "go.sum"}

// tree is the content, by path from the module root, of the files the verdicts of mutants in the
// package directories dirs (named from the module root) can rest on besides the toolchain: every
// file of those directories and of the directories below them (sources, tests, testdata, embedded
// files), save the directories the go command never reads a package's files from (a name that
// starts with "." or "_", a nested module), and the module's go.mod and go.sum.
func (m MutantExecutorDealer) tree(dirs []string) (map[string][]byte, error) {
	fsys := os.DirFS(m.mod.Root)
	files := map[string][]byte{}
	for _, dir := range dirs {
		if !fs.ValidPath(dir) {
			return nil, fmt.Errorf("%s is outside the module", dir)
		}
		if err := walk(fsys, dir, files); err != nil {
			return nil, err
		}
	}
	for _, name := range moduleFiles {
		src, err := fs.ReadFile(fsys, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files[name] = src
	}

	return files, nil
}

// walk adds to files the content of every regular file of the directory dir of fsys and of the
// directories below it that tree reads, by its path in fsys.
func walk(fsys fs.FS, dir string, files map[string][]byte) error {
	return fs.WalkDir(fsys, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name != dir && skippedDir(fsys, name) {
				return fs.SkipDir
			}

			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		files[name] = src

		return nil
	})
}

// skippedDir reports whether the directory name of fsys, below a package directory, holds no file
// of it: a name that starts with "." or "_", which the go command ignores, or a nested module.
func skippedDir(fsys fs.FS, name string) bool {
	base := path.Base(name)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return true
	}
	_, err := fs.Stat(fsys, path.Join(name, "go.mod"))

	return err == nil
}

// mutantDirs are the package directories, from the module root, of the deferred mutants dms.
func (m MutantExecutorDealer) mutantDirs(dms []DeferredMutant) []string {
	var dirs []string
	for _, dm := range dms {
		if dir := path.Dir(m.fromRoot(dm.File)); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}

	return dirs
}

// deferredFiles are the files of a tree with their digests, by path.
func deferredFiles(files map[string][]byte) []DeferredFile {
	out := make([]DeferredFile, 0, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		out = append(out, DeferredFile{Path: name, SHA256: digest(files[name])})
	}

	return out
}

// sameFiles checks that the tree files holds the files recorded, each with the content it had.
func sameFiles(recorded []DeferredFile, files map[string][]byte) error {
	seen := map[string]bool{}
	for _, f := range recorded {
		src, ok := files[f.Path]
		if !ok {
			return fmt.Errorf("%s is gone since the run that deferred its mutants", f.Path)
		}
		if digest(src) != f.SHA256 {
			return fmt.Errorf("%s changed since the run that deferred its mutants", f.Path)
		}
		seen[f.Path] = true
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !seen[name] {
			return fmt.Errorf("%s is new since the run that deferred its mutants", name)
		}
	}

	return nil
}
