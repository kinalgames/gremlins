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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

// scan is a read of a module's files: the hex SHA-256 of each file it reads, by path from the
// module root, and the content of the files it keeps.
type scan struct {
	fsys     fs.FS
	keep     map[string]bool   // the files whose content the scan keeps
	own      func(string) bool // the files gremlins' runs write themselves, which it leaves out
	digests  map[string]string
	contents map[string][]byte
}

// tree is the scan of the files the verdicts of mutants in the package directories dirs (named
// from the module root) can rest on besides the toolchain: every file of those directories and of
// the directories below them (sources, tests, testdata, embedded files), save the directories the
// go command never reads a package's files from (a name that starts with "." or "_", a nested
// module), and the module's go.mod and go.sum; save, too, the files own names. It keeps the
// content of the files keep names.
func (m MutantExecutorDealer) tree(dirs []string, keep map[string]bool, own func(string) bool) (*scan, error) {
	s := &scan{fsys: os.DirFS(m.mod.Root), keep: keep, own: own, digests: map[string]string{}, contents: map[string][]byte{}}
	for _, dir := range dirs {
		if !fs.ValidPath(dir) {
			return nil, fmt.Errorf("%s is outside the module", dir)
		}
		if err := s.walk(dir); err != nil {
			return nil, err
		}
	}
	for _, name := range moduleFiles {
		if err := s.add(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	return s, nil
}

// walk adds every regular file of the directory dir and of the directories below it that tree
// reads.
func (s *scan) walk(dir string) error {
	return fs.WalkDir(s.fsys, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name != dir && skippedDir(s.fsys, name) {
				return fs.SkipDir
			}

			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		return s.add(name)
	})
}

// add hashes the file name as it reads it, and keeps its content when the scan keeps it; a file
// gremlins' runs write is left out.
func (s *scan) add(name string) error {
	if s.own(name) {
		return nil
	}
	f, err := s.fsys.Open(name)
	if err != nil {
		return err
	}
	h := sha256.New()
	var kept bytes.Buffer
	var w io.Writer = h
	if s.keep[name] {
		w = io.MultiWriter(h, &kept)
	}
	_, err = io.Copy(w, f)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	s.digests[name] = hex.EncodeToString(h.Sum(nil))
	if s.keep[name] {
		s.contents[name] = kept.Bytes()
	}

	return nil
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

// written is whether a file, by path from the module root, is one gremlins' runs write themselves:
// the file at the absolute path settle, the temporary files beside it that WriteDeferred renames
// to it, and the files at the absolute paths outputs. A path outside the module, relative or empty
// names no file of it.
func (m MutantExecutorDealer) written(settle string, outputs ...string) func(string) bool {
	files := map[string]bool{}
	temp := ""
	for i, p := range append([]string{settle}, outputs...) {
		rel, err := filepath.Rel(m.mod.Root, p)
		if p == "" || err != nil || !filepath.IsLocal(rel) {
			continue
		}
		rel = filepath.ToSlash(rel)
		files[rel] = true
		if i == 0 {
			temp = rel + "."
		}
	}

	return func(name string) bool {
		return files[name] || temp != "" && strings.HasPrefix(name, temp)
	}
}

// deferredFiles are the files of a scan with their digests, by path.
func deferredFiles(s *scan) []DeferredFile {
	out := make([]DeferredFile, 0, len(s.digests))
	for _, name := range slices.Sorted(maps.Keys(s.digests)) {
		out = append(out, DeferredFile{Path: name, SHA256: s.digests[name]})
	}

	return out
}

// sameFiles checks that the scan s holds the files recorded, each with the content it had.
func sameFiles(recorded []DeferredFile, s *scan) error {
	seen := map[string]bool{}
	for _, f := range recorded {
		sum, ok := s.digests[f.Path]
		if !ok {
			return fmt.Errorf("%s is gone since the run that deferred its mutants", f.Path)
		}
		if sum != f.SHA256 {
			return fmt.Errorf("%s changed since the run that deferred its mutants", f.Path)
		}
		seen[f.Path] = true
	}
	for _, name := range slices.Sorted(maps.Keys(s.digests)) {
		if !seen[name] {
			return fmt.Errorf("%s is new since the run that deferred its mutants", name)
		}
	}

	return nil
}
