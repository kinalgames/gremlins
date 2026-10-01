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
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-gremlins/gremlins/internal/gomodule"
	"github.com/go-gremlins/gremlins/internal/mutator"
)

// TextMutator is a mutator.Mutator the user writes (mutator.Extra): in its file, the only
// occurrence of a text becomes another. It stands for a mutation Gremlins cannot find or cannot
// see covered, such as the condition of a case in a tagless switch or a package-level constant,
// and runs whatever the coverage says.
type TextMutator struct {
	pkg      string
	position token.Position
	original []byte
	mutated  []byte
	workDir  string
	origFile []byte
	status   mutator.Status
}

// NewTextMutant is the mutant that replaces original with mutated in file, named from the calling
// directory of mod, or an error when the file cannot be read, lies outside the module, or does not
// hold original exactly once.
func NewTextMutant(mod gomodule.GoModule, file, original, mutated string) (*TextMutator, error) {
	name, err := moduleFile(mod, file)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(filepath.Join(mod.Root, mod.CallingDir, name)) //nolint:gosec // a file of the module under test
	if err != nil {
		return nil, err
	}

	return newTextMutant(mod, file, original, mutated, src)
}

// moduleFile is file, named from the calling directory of mod, cleaned and with slashes, or an error
// when it lies outside the module.
func moduleFile(mod gomodule.GoModule, file string) (string, error) {
	file = filepath.ToSlash(filepath.Clean(filepath.FromSlash(file)))
	if !filepath.IsLocal(filepath.Join(mod.CallingDir, file)) {
		return "", fmt.Errorf("%s is outside the module", file)
	}

	return file, nil
}

// newTextMutant is NewTextMutant on src, the content of file.
func newTextMutant(mod gomodule.GoModule, file, original, mutated string, src []byte) (*TextMutator, error) {
	file, err := moduleFile(mod, file)
	if err != nil {
		return nil, err
	}
	if original == "" {
		return nil, fmt.Errorf("%s: the text to replace is empty", file)
	}
	at, err := onlyOccurrence(src, []byte(original))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	before := src[:at]

	return &TextMutator{
		pkg: packageOf(mod, file),
		position: token.Position{
			Filename: file, Offset: at,
			Line:   bytes.Count(before, []byte("\n")) + 1,
			Column: at - bytes.LastIndexByte(before, '\n'),
		},
		original: []byte(original), mutated: []byte(mutated), status: mutator.Runnable,
	}, nil
}

// onlyOccurrence is the offset of text in src, or an error unless src holds it exactly once.
func onlyOccurrence(src, text []byte) (int, error) {
	if n := bytes.Count(src, text); n != 1 {
		return 0, fmt.Errorf("%q occurs %d times, not once", text, n)
	}

	return bytes.Index(src, text), nil
}

// ReadExtraMutants reads the mutants the file path lists for the module mod (--extra-mutants),
// one per line, `file|original|mutated`: in the file, named from the calling directory, the only
// occurrence of original becomes mutated. An empty line is skipped. It answers an error for a
// line of another shape or a mutant NewTextMutant refuses.
func ReadExtraMutants(path string, mod gomodule.GoModule) ([]mutator.Mutator, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the file the user names
	if err != nil {
		return nil, err
	}
	var muts []mutator.Mutator
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s:%d: want file|original|mutated", path, i+1)
		}
		mut, err := NewTextMutant(mod, fields[0], fields[1], fields[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		muts = append(muts, mut)
	}

	return muts, nil
}

// Type is mutator.Extra.
func (*TextMutator) Type() mutator.Type {
	return mutator.Extra
}

// SetType does nothing: a TextMutator is always mutator.Extra.
func (*TextMutator) SetType(mutator.Type) {}

// Status returns the mutator.Status of the mutant.
func (m *TextMutator) Status() mutator.Status {
	return m.status
}

// SetStatus sets the mutator.Status of the mutant.
func (m *TextMutator) SetStatus(s mutator.Status) {
	m.status = s
}

// Position is where the replaced text starts, its file named from the calling directory.
func (m *TextMutator) Position() token.Position {
	return m.position
}

// Pos is token.NoPos: the mutant belongs to no parsed file.
func (*TextMutator) Pos() token.Pos {
	return token.NoPos
}

// Pkg returns the import path of the mutant's package.
func (m *TextMutator) Pkg() string {
	return m.pkg
}

// Original is the text the mutant replaces.
func (m *TextMutator) Original() string {
	return string(m.original)
}

// Mutated is the text the mutant puts in its place.
func (m *TextMutator) Mutated() string {
	return string(m.mutated)
}

// SetWorkdir sets the copy of the calling directory Apply and Rollback work in.
func (m *TextMutator) SetWorkdir(path string) {
	m.workDir = path
}

// Workdir returns the copy of the calling directory Apply and Rollback work in.
func (m *TextMutator) Workdir() string {
	return m.workDir
}

// Apply replaces the text in the file of the work directory, keeping the file for Rollback; the
// copy must hold the text exactly once, as the source did.
func (m *TextMutator) Apply() error {
	filename := filepath.Join(m.workDir, m.position.Filename)
	src, err := os.ReadFile(filename) //nolint:gosec // a file of a copy of the module
	if err != nil {
		return err
	}
	at, err := onlyOccurrence(src, m.original)
	if err != nil {
		return err
	}
	if at != m.position.Offset {
		return errors.New("the copy holds the text elsewhere than the source")
	}
	m.origFile = src

	return os.WriteFile(filename, slices.Concat(src[:at], m.mutated, src[at+len(m.original):]), 0o600) //nolint:gosec // a file of a copy of the module
}

// Rollback puts back the file Apply mutated.
func (m *TextMutator) Rollback() error {
	defer func() { m.origFile = nil }()

	return os.WriteFile(filepath.Join(m.workDir, m.position.Filename), m.origFile, 0o600)
}
