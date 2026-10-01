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
	"errors"
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

// builder indexes the module in testdata/name with the go command, its tests running in the
// fixture itself.
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
		Tree:        func() (string, error) { return root, nil },
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

// patternCase is the pattern the index of a fixture answers for the first occurrence of text.
type patternCase struct{ text, pattern string }

func checkPatterns(t *testing.T, ix selection.Index, root, file string, cases []patternCase) {
	t.Helper()
	for _, tc := range cases {
		if pattern := ix.Pattern(positionOf(t, root, file, tc.text)); pattern != tc.pattern {
			t.Errorf("%s: %q, want %q", tc.text, pattern, tc.pattern)
		}
	}
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
	checkPatterns(t, ix, root, "initmod.go", []patternCase{
		{"n * 2", ""}, // a package-level variable's initialiser: every test covers it
		{"n * 3", ""}, // init: every test covers it
		{"a + b", "^(TestAdd)$"},
		{"a - b", "^(TestSub|ExampleSub)$"},
		{"a + 1", ""}, // no test: every test runs, and the package's coverage says NOT COVERED
	})
}

// kindsTests are the runnable names the kinds fixture's test binary lists without build tags:
// tests of the package and of its external test package, with subtests, and a fuzz target; not
// the line its TestMain prints.
var kindsTests = []string{"FuzzSize", "TestAdd", "TestAddMore", "TestHalf", "TestNeg"}

func TestEveryKindOfTestIsIndexedByItsOwnName(t *testing.T) {
	b, root := builder(t, "kinds")
	ix, err := b.Index("example.com/kinds")
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(slices.Values(ix.Tests)); !slices.Equal(got, kindsTests) || len(ix.Profiles) != len(kindsTests) {
		t.Fatalf("indexed %v (%d profiles), want %v", ix.Tests, len(ix.Profiles), kindsTests)
	}
	checkPatterns(t, ix, root, "kinds.go", []patternCase{
		{"a + b", "^(TestAdd)$"},
		{"a * 10", "^(TestAddMore)$"}, // TestAdd's own run does not run TestAddMore
		{"n / 2", "^(TestHalf)$"},
		{"return -n", "^(TestNeg)$"},
		{"len(s) - 1", "^(FuzzSize)$"},
		{"n % 7", ""}, // TestTagged is not built without its tag
	})
}

func TestTheIndexBuildsWithTheBuildTags(t *testing.T) {
	b, root := builder(t, "kinds")
	b.Tags = "extra"
	ix, err := b.Index("example.com/kinds")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ix.Tests, "TestTagged") {
		t.Fatalf("indexed %v", ix.Tests)
	}
	checkPatterns(t, ix, root, "kinds.go", []patternCase{{"n % 7", "^(TestTagged)$"}})
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
	b, _ := builder(t, "broken")
	if _, err := b.Index("example.com/broken"); err == nil || !strings.Contains(err.Error(), "go test -c") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPackageIsNotIndexedWithoutItsTreeOrAWorkDirectory(t *testing.T) {
	b, root := builder(t, "initmod")
	b.Tree = func() (string, error) { return filepath.Join(root, "absent"), nil }
	if _, err := b.Index("example.com/initmod"); err == nil {
		t.Error("indexed in an absent tree")
	}
	boom := errors.New("no copy")
	b.Tree = func() (string, error) { return "", boom }
	if _, err := b.Index("example.com/initmod"); !errors.Is(err, boom) {
		t.Errorf("without a copy: %v", err)
	}
	b, _ = builder(t, "initmod")
	b.WorkDir = filepath.Join(b.WorkDir, "absent")
	if _, err := b.Index("example.com/initmod"); err == nil {
		t.Error("indexed without a work directory")
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

func TestASubsetRunsTogetherOnTheUnmutatedBinary(t *testing.T) {
	b, _ := builder(t, "pollute")
	ix, err := b.Index("example.com/pollute")
	if err != nil {
		t.Fatal(err) // each test passes alone
	}
	if err := b.Passes(ix, "^(TestA|TestB|TestC)$"); err != nil {
		t.Errorf("the whole suite: %v", err)
	}
	if err := b.Passes(ix, "^(TestA|TestC)$"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("TestC after TestA: %v", err)
	}
}
