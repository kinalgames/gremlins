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
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/engine"
	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// The module of the text-mutant tests: its package and its file.
const (
	subPkg   = "example.com/m/sub"
	fileQ    = "q.go"
	fileSubQ = "sub/q.go"
)

// switchSource has a tagless switch whose case conditions Gremlins cannot see covered, below a
// constant at line 3.
const switchSource = "package q\n\nconst Limit = 10\n\nfunc Fits(n int) bool {\n\tswitch {\n\tcase n <= Limit:\n\t\treturn true\n\t}\n\n\treturn false\n}\n"

// switchModule is a module whose package sub holds switchSource in q.go, called from calling.
func switchModule(t *testing.T, calling string) gomodule.GoModule {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", fileQ), []byte(switchSource), 0o600); err != nil {
		t.Fatal(err)
	}

	return gomodule.GoModule{Name: "example.com/m", Root: root, CallingDir: calling}
}

func TestATextMutantIsWhereItsTextIs(t *testing.T) {
	cases := []struct {
		calling, file, original string
		want                    token.Position
		pkg                     string
	}{
		{".", fileSubQ, "case n <= Limit:", token.Position{Filename: fileSubQ, Offset: 64, Line: 7, Column: 2}, subPkg},
		{"sub", fileQ, "const Limit = 10", token.Position{Filename: fileQ, Offset: 11, Line: 3, Column: 1}, subPkg},
		{"sub", "./q.go", "package q", token.Position{Filename: fileQ, Offset: 0, Line: 1, Column: 1}, subPkg},
	}
	for _, tc := range cases {
		mut, err := engine.NewTextMutant(switchModule(t, tc.calling), tc.file, tc.original, "x")
		if err != nil {
			t.Fatalf("%s: %v", tc.original, err)
		}
		if mut.Position() != tc.want || mut.Pkg() != tc.pkg || mut.Status() != mutator.Runnable || mut.Type() != mutator.Extra {
			t.Errorf("%s: %v %s %v %v, want %v %s", tc.original, mut.Position(), mut.Pkg(), mut.Status(), mut.Type(), tc.want, tc.pkg)
		}
		if mut.Original() != tc.original || mut.Mutated() != "x" || mut.Pos() != token.NoPos {
			t.Errorf("%s: %q -> %q at %v", tc.original, mut.Original(), mut.Mutated(), mut.Pos())
		}
	}
}

func TestATextMutantMustNameTextThatOccursOnceInTheModule(t *testing.T) {
	cases := []struct {
		file, original, want string
	}{
		{fileSubQ, "return", `"return" occurs 2 times, not once`},
		{fileSubQ, "Limits", `"Limits" occurs 0 times, not once`},
		{fileSubQ, "", "the text to replace is empty"},
		{"sub/none.go", "x", "none.go"},
		{"../q.go", "x", "outside the module"},
		{"sub/Q.go", "x", "sub/Q.go names Q.go, which the disk names q.go"},
		{"Sub/q.go", "x", "Sub/q.go names Sub, which the disk names sub"},
		{"missing/q.go", "x", "missing"},
	}
	for _, tc := range cases {
		if _, err := engine.NewTextMutant(switchModule(t, "."), tc.file, tc.original, "x"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: %v, want %q", tc.file, tc.original, err, tc.want)
		}
	}
}

func TestATextMutantChangesItsCopyAndPutsItBack(t *testing.T) {
	mod := switchModule(t, "sub")
	mut, err := engine.NewTextMutant(mod, fileQ, "n <= Limit", "n < Limit")
	if err != nil {
		t.Fatal(err)
	}
	mut.SetType(mutator.ArithmeticBase)
	mut.SetStatus(mutator.Lived)
	if mut.Type() != mutator.Extra || mut.Status() != mutator.Lived {
		t.Errorf("%v %v", mut.Type(), mut.Status())
	}
	copied := filepath.Join(t.TempDir(), "sub")
	if err := os.CopyFS(copied, os.DirFS(filepath.Join(mod.Root, "sub"))); err != nil {
		t.Fatal(err)
	}
	mut.SetWorkdir(copied)
	if mut.Workdir() != copied {
		t.Fatalf("workdir %s", mut.Workdir())
	}
	if err := mut.Apply(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, copied); got != strings.Replace(switchSource, "n <= Limit", "n < Limit", 1) {
		t.Errorf("applied: %q", got)
	}
	if err := mut.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, copied); got != switchSource {
		t.Errorf("rolled back: %q", got)
	}
}

func TestATextMutantRefusesACopyThatDiffersFromTheSource(t *testing.T) {
	copies := []struct {
		name, content, want string
	}{
		{"the text twice", switchSource + "// n <= Limit\n", "occurs 2 times"},
		{"the text elsewhere", "\n" + switchSource, "elsewhere than the source"},
	}
	for _, tc := range copies {
		mut, err := engine.NewTextMutant(switchModule(t, "sub"), fileQ, "n <= Limit", "n < Limit")
		if err != nil {
			t.Fatal(err)
		}
		copied := t.TempDir()
		if err := os.WriteFile(filepath.Join(copied, fileQ), []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		mut.SetWorkdir(copied)
		if err := mut.Apply(); err == nil || !strings.Contains(err.Error(), tc.want) || read(t, copied) != tc.content {
			t.Errorf("%s: %v, want %q and the copy untouched", tc.name, err, tc.want)
		}
	}
	mut, err := engine.NewTextMutant(switchModule(t, "sub"), fileQ, "n <= Limit", "n < Limit")
	if err != nil {
		t.Fatal(err)
	}
	mut.SetWorkdir(t.TempDir())
	if err := mut.Apply(); err == nil {
		t.Error("applied in a copy without the file")
	}
}

// read is q.go in dir.
func read(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, fileQ)) //nolint:gosec // the test's own temporary copy
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestExtraMutantsAreReadOnePerLine(t *testing.T) {
	mod := switchModule(t, "sub")
	path := filepath.Join(t.TempDir(), "mutants.txt")
	list := "q.go|const Limit = 10|const Limit = 11\r\n\nq.go|n <= Limit|n < Limit|x\n"
	if err := os.WriteFile(path, []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	muts, err := engine.ReadExtraMutants(path, mod)
	if err != nil || len(muts) != 2 {
		t.Fatalf("%v: %v", muts, err)
	}
	first, second := muts[0].(*engine.TextMutator), muts[1].(*engine.TextMutator) //nolint:forcetypeassert // ReadExtraMutants answers TextMutators
	if first.Position().Line != 3 || first.Mutated() != "const Limit = 11" || second.Position().Line != 7 || second.Mutated() != "n < Limit|x" {
		t.Errorf("%v %q, %v %q", first.Position(), first.Mutated(), second.Position(), second.Mutated())
	}
	if first.ListLine() != 1 || second.ListLine() != 3 {
		t.Errorf("from lines %d and %d of the list, want 1 and 3", first.ListLine(), second.ListLine())
	}
}

func TestExtraMutantsMustEachBeOneMutantOfTheModule(t *testing.T) {
	lists := []struct {
		list, want string
	}{
		{"q.go|const Limit = 10\n", ":1: want file|original|mutated"},
		{"q.go|const Limit = 10|x\nq.go|return|x\n", `:2: q.go: "return" occurs 2 times`},
		{"q.go|const Limit = 10|x\nQ.go|return|x\n", ":2: Q.go names Q.go, which the disk names q.go"},
	}
	for _, tc := range lists {
		path := filepath.Join(t.TempDir(), "mutants.txt")
		if err := os.WriteFile(path, []byte(tc.list), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.ReadExtraMutants(path, switchModule(t, "sub")); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.list, err, tc.want)
		}
	}
	if _, err := engine.ReadExtraMutants(filepath.Join(t.TempDir(), "none.txt"), switchModule(t, "sub")); err == nil {
		t.Error("read a list that does not exist")
	}
}

func TestTheEngineRunsTheExtraMutantsWhateverTheCoverage(t *testing.T) {
	mut, err := engine.NewTextMutant(switchModule(t, "."), fileSubQ, "const Limit = 10", "const Limit = 11")
	if err != nil {
		t.Fatal(err)
	}
	viperSet(map[string]any{configuration.UnleashDryRunKey: true})
	defer viperReset()
	mapFS, mod, c := loadFixture(defaultFixture, ".")
	defer c()
	e := engine.New(mod, engine.CodeData{}, newJobDealerStub(t), engine.WithDirFs(mapFS), engine.WithExtraMutants([]mutator.Mutator{mut}))
	res := e.Run(context.Background())
	extras := 0
	for _, m := range res.Mutants {
		switch {
		case m == mutator.Mutator(mut) && m.Status() == mutator.Runnable:
			extras++
		case m.Status() != mutator.NotCovered:
			t.Errorf("%v %v at %v without coverage", m.Status(), m.Type(), m.Position())
		}
	}
	if extras != 1 || len(res.Mutants) < 2 {
		t.Errorf("the extra mutant reported %d times among %d", extras, len(res.Mutants))
	}
}
