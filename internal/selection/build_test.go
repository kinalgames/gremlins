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

package selection_test

import (
	"context"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// builder indexes the module in testdata/name with the go command.
func builder(t *testing.T, name string) (selection.GoBuilder, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")

	return selection.GoBuilder{
		Exec:        exec.CommandContext,
		Module:      gomodule.GoModule{Name: "example.com/" + name, Root: root, CallingDir: "."},
		WorkDir:     t.TempDir(),
		Parallelism: 2,
		Timeout:     2 * time.Minute,
	}, root
}

// positionOf is the position of the first occurrence of text in the module's file name.
func positionOf(t *testing.T, root, name, text string) token.Position {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // a file of a fixture
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		if col := strings.Index(line, text); col >= 0 {
			return token.Position{Filename: name, Line: i + 1, Column: col + 1}
		}
	}
	t.Fatalf("%q is not in %s", text, name)

	return token.Position{}
}

func TestEachTestIsIndexedAloneAndInitialisationCoversEveryTest(t *testing.T) {
	b, root := builder(t, "initmod")
	ix, err := b.Index("example.com/initmod")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestAdd", "TestSub", "ExampleSub"}; !slices.Equal(ix.Tests, want) || len(ix.Profiles) != len(want) {
		t.Fatalf("indexed %v (%d profiles), want %v", ix.Tests, len(ix.Profiles), want)
	}
	cases := []struct {
		text, pattern string
		ok            bool
	}{
		{"n * 2", "^(TestAdd|TestSub|ExampleSub)$", true}, // a package-level variable's initialiser
		{"n * 3", "^(TestAdd|TestSub|ExampleSub)$", true}, // init
		{"a + b", "^(TestAdd)$", true},
		{"a - b", "^(TestSub|ExampleSub)$", true},
		{"a + 1", "", false}, // no test: every test runs, and the package's coverage says NOT COVERED
	}
	for _, tc := range cases {
		if pattern, ok := ix.Pattern(positionOf(t, root, "initmod.go", tc.text)); pattern != tc.pattern || ok != tc.ok {
			t.Errorf("%s: %q, %t; want %q, %t", tc.text, pattern, ok, tc.pattern, tc.ok)
		}
	}
}

func TestAPackageWithATestThatFailsAloneIsNotIndexed(t *testing.T) {
	b, _ := builder(t, "orderdep")
	if _, err := b.Index("example.com/orderdep"); err == nil || !strings.Contains(err.Error(), "TestSecond alone") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPackageWithoutTestsHasAnEmptyIndex(t *testing.T) {
	b, _ := builder(t, "notests")
	ix, err := b.Index("example.com/notests")
	if err != nil || len(ix.Tests) != 0 {
		t.Fatalf("index %v, err %v", ix, err)
	}
}

func TestAPackageThatDoesNotBuildIsNotIndexed(t *testing.T) {
	b, _ := builder(t, "initmod")
	b.Tags = "no such tag"
	b.Module.Root = filepath.Join(b.Module.Root, "absent")
	if _, err := b.Index("example.com/initmod"); err == nil {
		t.Fatal("an absent package was indexed")
	}
	b, _ = builder(t, "initmod")
	b.WorkDir = filepath.Join(b.WorkDir, "absent")
	if _, err := b.Index("example.com/initmod"); err == nil {
		t.Fatal("indexed without a work directory")
	}
}

func TestATestBoundByTheTimeoutIsNotIndexed(t *testing.T) {
	b, _ := builder(t, "initmod")
	b.Exec = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "go" && slices.Contains(args, "-test.run") {
			ctx, cancel := context.WithTimeout(ctx, 0)
			t.Cleanup(cancel)

			return exec.CommandContext(ctx, name, args...) //nolint:gosec // the fixture's test binary
		}

		return exec.CommandContext(ctx, name, args...) //nolint:gosec // the go command or the fixture's test binary
	}
	if _, err := b.Index("example.com/initmod"); err == nil {
		t.Fatal("a test past its timeout was indexed")
	}
}

// withRun is b, whose test binary runs with args in place of those, of its own, that hold flag.
func withRun(b selection.GoBuilder, flag string, args ...string) selection.GoBuilder {
	b.Exec = func(ctx context.Context, name string, given ...string) *exec.Cmd {
		if name != "go" && slices.Contains(given, flag) {
			return exec.CommandContext(ctx, name, args...) //nolint:gosec // the fixture's test binary
		}

		return exec.CommandContext(ctx, name, given...) //nolint:gosec // the go command or the fixture's test binary
	}

	return b
}

func TestABinaryThatCannotListItsTestsIsNotIndexed(t *testing.T) {
	b, _ := builder(t, "initmod")
	if _, err := withRun(b, "-test.list", "-test.no-such-flag").Index("example.com/initmod"); err == nil {
		t.Fatal("indexed without its list")
	}
}

func TestATestThatWritesNoProfileIsNotIndexed(t *testing.T) {
	b, _ := builder(t, "initmod")
	if _, err := withRun(b, "-test.run", "-test.run", "^$").Index("example.com/initmod"); err == nil {
		t.Fatal("indexed without profiles")
	}
}
