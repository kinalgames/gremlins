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

package selection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/gomodule"
)

// ExecContext starts a command; exec.CommandContext, or a fake in tests.
type ExecContext = func(ctx context.Context, name string, args ...string) *exec.Cmd

// GoBuilder indexes a package's tests with the go command: it builds the package's test binary
// with coverage, lists its tests, examples and fuzz targets, and runs each alone, in the
// package's directory of a copy of the module (Tree), Parallelism at a time.
type GoBuilder struct {
	Exec        ExecContext
	Module      gomodule.GoModule
	Tree        func() (string, error) // the root of the copy of the module the tests run in
	WorkDir     string                 // where the binaries and profiles go
	Tags        string                 // the build tags of the test binary
	Parallelism int                    // how many tests run at once
	Timeout     time.Duration          // how long one test's run may take
}

// waitDelay bounds how long a command's output is awaited after it ends or is stopped: a child it
// left running would otherwise hold the output open, past the timeout.
const waitDelay = 5 * time.Second

// Index indexes the tests of the package pkg, a package of the module. A package without tests
// has an empty index.
func (b GoBuilder) Index(pkg string) (Index, error) {
	root, err := b.Tree()
	if err != nil {
		return Index{}, err
	}
	dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(pkg, b.Module.Name), "/")))
	out, err := os.MkdirTemp(b.WorkDir, "selection-")
	if err != nil {
		return Index{}, err
	}
	bin := filepath.Join(out, "package.test")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args := []string{"test", "-c", "-cover", "-covermode=set", "-o", bin}
	if b.Tags != "" {
		args = append(args, "-tags", b.Tags)
	}
	if _, err := b.run(dir, "go", append(args, ".")...); err != nil {
		return Index{}, err
	}
	if _, err := os.Stat(bin); errors.Is(err, fs.ErrNotExist) {
		return Index{}, nil
	}
	listed, err := b.run(dir, bin, "-test.list", ".*")
	if err != nil {
		return Index{}, err
	}
	tests := testNames(listed)
	profiles, err := b.profiles(dir, bin, out, tests)
	if err != nil {
		return Index{}, err
	}

	return Index{Tests: tests, Profiles: profiles, Binary: bin, Dir: dir}, nil
}

// Passes runs the tests pattern selects in the index's unmutated binary; nil when they pass.
func (b GoBuilder) Passes(ix Index, pattern string) error {
	_, err := b.run(ix.Dir, ix.Binary, "-test.run", pattern)

	return err
}

// runnable is a name -test.list prints that -test.run runs: a test, an example or a fuzz target
// (whose seed corpus runs as a test), not a benchmark; a whole Go identifier, so that a line
// TestMain prints is not taken for a test.
var runnable = regexp.MustCompile(`^(Test|Example|Fuzz)[\p{L}\p{N}_]*$`)

// testNames are the runnable names of the output of -test.list.
func testNames(listed []byte) []string {
	var names []string
	for _, line := range strings.Split(string(listed), "\n") {
		if runnable.MatchString(line) {
			names = append(names, line)
		}
	}

	return names
}

// profiles runs each test of tests alone with a coverage profile and reads the profiles.
func (b GoBuilder) profiles(dir, bin, out string, tests []string) ([]coverage.Profile, error) {
	profiles := make([]coverage.Profile, len(tests))
	errs := make([]error, len(tests))
	slots := make(chan struct{}, max(b.Parallelism, 1))
	var wg sync.WaitGroup
	for i, name := range tests {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			file := filepath.Join(out, strconv.Itoa(i)+".cover")
			if _, err := b.run(dir, bin, "-test.run", "^"+regexp.QuoteMeta(name)+"$", "-test.coverprofile", file); err != nil {
				errs[i] = fmt.Errorf("%s alone: %w", name, err)

				return
			}
			profiles[i], errs[i] = readProfile(file, b.Module)
		}()
	}
	wg.Wait()

	return profiles, errors.Join(errs...)
}

// readProfile reads the coverage profile file of the module mod.
func readProfile(file string, mod gomodule.GoModule) (coverage.Profile, error) {
	f, err := os.Open(file) //nolint:gosec // a profile this builder's test run wrote
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return coverage.Parse(f, mod)
}

// run runs name with args in dir within the builder's timeout and answers its output; an error
// carries the end of the output.
func (b GoBuilder) run(dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.Timeout)
	defer cancel()
	cmd := b.Exec(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", filepath.Base(name), strings.Join(args, " "), err, tail(stdout.String()+stderr.String()))
	}

	return stdout.Bytes(), nil
}

// tail is the last lines of output, which say why a command failed.
func tail(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	return strings.Join(lines[max(len(lines)-20, 0):], "\n")
}
