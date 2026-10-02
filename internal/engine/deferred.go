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
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// Deferred is what a run hands to a later run that settles its pending mutants (Settler) once
// nothing else runs on the machine, as when several gremlins runs share it: the time each
// mutant's tests get, the settings that change a verdict, the files its verdicts can rest on with
// their digests (tree), the packages whose test selection was disabled, each pending mutant, and
// the absolute path of the run's report (-o), which a settling run finds new since the run.
type Deferred struct {
	Timeout  time.Duration    `json:"timeout"`
	Settings Settings         `json:"settings"`
	Files    []DeferredFile   `json:"files"`
	Disabled []string         `json:"disabled,omitempty"`
	Mutants  []DeferredMutant `json:"mutants"`
	Output   string           `json:"output,omitempty"`
}

// Settings are the settings of a run that change a mutant's verdict: the package directory the
// run was given (the calling directory, from the module root), the build tags, the integration
// mode, the -cpu of the tests, the coverage packages and the toolchain. A run settles another's
// mutants only with the same settings.
type Settings struct {
	Package     string    `json:"package"`
	Tags        string    `json:"tags,omitempty"`
	Integration bool      `json:"integration,omitempty"`
	TestCPU     int       `json:"test_cpu,omitempty"`
	CoverPkg    string    `json:"coverpkg,omitempty"`
	Toolchain   Toolchain `json:"toolchain"`
}

// DeferredFile is a file a pending mutant's verdict can rest on, named from the module root, with
// the hex SHA-256 of its content when the run deferred the mutant. A run settles the mutant only
// in the same files with the same content.
type DeferredFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// DeferredMutant is a pending mutant: its position (the file named from the calling directory),
// its type and package; for a mutator.Extra, the text it replaces, the text it puts in its place
// and the line of the list that wrote it; First, for a mutant that runs again alone, how its first
// run ended; Kill for a subset kill the whole suite has not confirmed, and Confirm for one Settle
// confirms before any other mutant.
type DeferredMutant struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Type     string `json:"type"`
	Pkg      string `json:"pkg"`
	Original string `json:"original,omitempty"`
	Mutated  string `json:"mutated,omitempty"`
	ListLine int    `json:"list_line,omitempty"`
	First    string `json:"first,omitempty"`
	Kill     bool   `json:"kill,omitempty"`
	Confirm  bool   `json:"confirm,omitempty"`
}

// Defer is the state Settle would need to settle pending, all of them pending (Pending), or an
// error when a file of their packages cannot be read for its digest. The tree records every file it
// finds, at the paths this run writes too: its settle file is removed when it starts and written
// after Defer, and its report (-o) after the run, so a file there is not this run's.
func (m MutantExecutorDealer) Defer(pending []mutator.Mutator) (Deferred, error) {
	confirm := m.unsettled.confirmations()
	pending = slices.Clone(pending)
	slices.SortFunc(pending, byID)
	d := Deferred{
		Timeout: m.testExecutionTime, Settings: m.settings(), Mutants: make([]DeferredMutant, 0, len(pending)),
		Output: configuration.Get[string](configuration.UnleashOutputKey),
	}
	m.unsettled.mu.Lock()
	defer m.unsettled.mu.Unlock()
	for _, mut := range pending {
		dm := deferredMutant(mut)
		k, killed := m.unsettled.kills[mut]
		dm.Kill, dm.Confirm = killed, slices.Contains(confirm, mut)
		if first, again := m.unsettled.first[mut]; again {
			dm.First = first.String()
		}
		d.Mutants = append(d.Mutants, dm)
		if killed && m.selector != nil && m.selector.Disabled(k.pkg) && !slices.Contains(d.Disabled, k.pkg) {
			d.Disabled = append(d.Disabled, k.pkg)
		}
	}
	s, err := m.tree(m.mutantDirs(d.Mutants), nil, func(string) bool { return false })
	if err != nil {
		return Deferred{}, err
	}
	d.Files = deferredFiles(s)

	return d, nil
}

// deferredMutant is mut's position, type, package and, for a TextMutator, its texts.
func deferredMutant(mut mutator.Mutator) DeferredMutant {
	pos := mut.Position()
	dm := DeferredMutant{File: filepath.ToSlash(pos.Filename), Line: pos.Line, Column: pos.Column, Type: mut.Type().String(), Pkg: mut.Pkg()}
	if tm, ok := mut.(*TextMutator); ok {
		dm.Original, dm.Mutated, dm.ListLine = tm.Original(), tm.Mutated(), tm.ListLine()
	}

	return dm
}

// settings are the dealer's Settings.
func (m MutantExecutorDealer) settings() Settings {
	return Settings{
		Package: filepath.ToSlash(m.mod.CallingDir), Tags: m.buildTags, Integration: m.integrationMode, TestCPU: m.testCPU,
		CoverPkg: configuration.Get[string](configuration.UnleashCoverPkgKey), Toolchain: m.toolchain,
	}
}

// fromRoot names file, named from the calling directory, from the module root.
func (m MutantExecutorDealer) fromRoot(file string) string {
	return filepath.ToSlash(filepath.Join(m.mod.CallingDir, filepath.FromSlash(file)))
}

// WriteDeferred writes d to the file path: to a temporary file beside it, renamed to path once
// complete, so that path never holds part of d. The temporary file does not outlive a failure.
func WriteDeferred(path string, d Deferred) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	err = errors.Join(json.NewEncoder(f).Encode(d), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}

	return nil
}

// RemoveDeferred removes the file path, so that no later run settles mutants this run did not
// defer: a run with --settle-later removes it before it starts and after a failed write. A path
// that does not exist is no error.
func RemoveDeferred(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// ReadDeferred reads the Deferred a run wrote to the file path.
func ReadDeferred(path string) (Deferred, error) {
	var d Deferred
	data, err := os.ReadFile(path) //nolint:gosec // the file the user names
	if err != nil {
		return d, err
	}

	return d, json.Unmarshal(data, &d)
}

// Resume gives the mutants a run deferred their verdicts, as that run's Settle would have: the
// mutants are found again by position and type in the content of their files the deferring run
// recorded, and their tests get the deferred time. It answers the mutants, or an error before any
// test runs when this run's settings or a mutated file differ from the deferring run's (sameRun),
// or a mutant is not in its file.
func (m *MutantExecutorDealer) Resume(ctx context.Context, d Deferred) ([]mutator.Mutator, error) {
	sources, err := m.sameRun(d)
	if err != nil {
		return nil, err
	}
	m.testExecutionTime = d.Timeout
	files := map[string]*parsedFile{}
	muts := make([]mutator.Mutator, 0, len(d.Mutants))
	for _, dm := range d.Mutants {
		mut, err := m.find(files, sources[m.fromRoot(dm.File)], dm)
		if err != nil {
			return nil, err
		}
		muts = append(muts, mut)
		first, again := outcomeNamed(dm.First)
		switch {
		case again:
			mut.SetStatus(first.status())
			m.unsettled.runAgain(mut, first)
		case dm.Kill:
			mut.SetStatus(mutator.Killed) // the subset's verdict, unless Settle confirms it otherwise
			m.unsettled.kill(mut, subsetKill{pkg: dm.Pkg, recheck: dm.Confirm})
		default:
			return nil, fmt.Errorf("%s:%d:%d %s is neither a run to repeat nor a kill to confirm", dm.File, dm.Line, dm.Column, dm.Type)
		}
	}
	m.unsettled.resumed = true
	if m.selector == nil { // a run without test selection still settles the kills of one with it
		m.selector = selection.New(nil, 0, reportSelection)
	}
	for _, pkg := range d.Disabled {
		m.selector.Restore(pkg)
	}
	m.Settle(ctx, muts)

	return muts, nil
}

// sameRun checks that this run settles d's mutants as the run that deferred them would have: with
// the same settings, among the same files of their packages and module (tree) with the same
// content, each mutant's file among them. The tree leaves out only the files the two runs write
// that d does not record, new since the deferring run: the settle file, its temporary files and
// the reports (written). It answers the content of the mutants' files by path from the module root.
func (m *MutantExecutorDealer) sameRun(d Deferred) (map[string][]byte, error) {
	if here := m.settings(); d.Settings != here {
		return nil, fmt.Errorf("the run that deferred the mutants had the settings %+v, this run has %+v", d.Settings, here)
	}
	keep := map[string]bool{}
	for _, dm := range d.Mutants {
		keep[m.fromRoot(dm.File)] = true
	}
	recorded := map[string]bool{}
	for _, f := range d.Files {
		recorded[f.Path] = true
	}
	written := m.written(configuration.Get[string](configuration.UnleashSettleKey), configuration.Get[string](configuration.UnleashOutputKey), d.Output)
	s, err := m.tree(m.mutantDirs(d.Mutants), keep, func(name string) bool { return written(name) && !recorded[name] })
	if err != nil {
		return nil, err
	}
	if err := sameFiles(d.Files, s); err != nil {
		return nil, err
	}
	for _, dm := range d.Mutants {
		if _, recorded := s.contents[m.fromRoot(dm.File)]; !recorded {
			return nil, fmt.Errorf("%s is not among the files the run that deferred the mutants recorded", dm.File)
		}
	}

	return s.contents, nil
}

// outcomeNamed is the outcome after which a mutant runs again alone that name describes, and
// whether there is one.
func outcomeNamed(name string) (outcome, bool) {
	for _, o := range []outcome{timedOut, crashed} {
		if o.String() == name {
			return o, true
		}
	}

	return passed, false
}

// parsedFile is a source file of the module, parsed once for all its mutants.
type parsedFile struct {
	set  *token.FileSet
	file *ast.File
}

// find is the mutant dm names, in src, the content of its file the deferring run recorded.
func (m *MutantExecutorDealer) find(files map[string]*parsedFile, src []byte, dm DeferredMutant) (mutator.Mutator, error) {
	if dm.Type == mutator.Extra.String() {
		return m.findText(src, dm)
	}
	pf, ok := files[dm.File]
	if !ok {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, filepath.FromSlash(dm.File), src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		pf = &parsedFile{set: set, file: file}
		files[dm.File] = pf
	}
	var found *TokenMutator
	ast.Inspect(pf.file, func(node ast.Node) bool {
		n, ok := NewTokenNode(node)
		if !ok || found != nil {
			return found == nil
		}
		pos := pf.set.Position(n.TokPos)
		for _, mt := range TokenMutantType[n.Tok()] {
			if pos.Line == dm.Line && pos.Column == dm.Column && mt.String() == dm.Type {
				found = NewTokenMutant(dm.Pkg, pf.set, pf.file, n)
				found.SetType(mt)
			}
		}

		return found == nil
	})
	if found == nil {
		return nil, fmt.Errorf("no %s mutant at %s:%d:%d", dm.Type, dm.File, dm.Line, dm.Column)
	}

	return found, nil
}

// findText is the mutator.Extra dm names: its texts, at its position in src, the content of its
// file the deferring run recorded.
func (m *MutantExecutorDealer) findText(src []byte, dm DeferredMutant) (mutator.Mutator, error) {
	tm, err := newTextMutant(m.mod, dm.File, dm.Original, dm.Mutated, src)
	if err != nil {
		return nil, err
	}
	if pos := tm.Position(); pos.Line != dm.Line || pos.Column != dm.Column {
		return nil, fmt.Errorf("no %s mutant at %s:%d:%d", dm.Type, dm.File, dm.Line, dm.Column)
	}
	tm.listLine = dm.ListLine

	return tm, nil
}
