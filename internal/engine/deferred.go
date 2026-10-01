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
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/selection"
)

// Deferred is what a run hands to a later run that settles its pending mutants (Settler) once
// nothing else runs on the machine, as when several gremlins runs share it: the time each
// mutant's tests get, the packages whose test selection was disabled, and each pending mutant.
type Deferred struct {
	Timeout  time.Duration    `json:"timeout"`
	Disabled []string         `json:"disabled,omitempty"`
	Mutants  []DeferredMutant `json:"mutants"`
}

// DeferredMutant is a pending mutant: its position (the file named from the calling directory),
// its type and package; First, for a mutant that runs again alone, how its first run ended; Kill
// for a subset kill the whole suite has not confirmed, and Confirm for one Settle confirms before
// any other mutant.
type DeferredMutant struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Type    string `json:"type"`
	Pkg     string `json:"pkg"`
	First   string `json:"first,omitempty"`
	Kill    bool   `json:"kill,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

// againNames name the outcomes after which a mutant runs again alone.
var againNames = map[outcome]string{timedOut: "timed out", crashed: "crashed"}

// Defer is the state Settle would need to settle pending, all of them pending (Pending).
func (m MutantExecutorDealer) Defer(pending []mutator.Mutator) Deferred {
	confirm := m.unsettled.confirmations()
	pending = slices.Clone(pending)
	slices.SortFunc(pending, byID)
	d := Deferred{Timeout: m.testExecutionTime, Mutants: make([]DeferredMutant, 0, len(pending))}
	m.unsettled.mu.Lock()
	defer m.unsettled.mu.Unlock()
	for _, mut := range pending {
		pos := mut.Position()
		k, killed := m.unsettled.kills[mut]
		d.Mutants = append(d.Mutants, DeferredMutant{
			File: filepath.ToSlash(pos.Filename), Line: pos.Line, Column: pos.Column, Type: mut.Type().String(), Pkg: mut.Pkg(),
			First: againNames[m.unsettled.first[mut]], Kill: killed, Confirm: slices.Contains(confirm, mut),
		})
		if killed && m.selector != nil && m.selector.Disabled(k.pkg) && !slices.Contains(d.Disabled, k.pkg) {
			d.Disabled = append(d.Disabled, k.pkg)
		}
	}

	return d
}

// WriteDeferred writes d to the file path.
func WriteDeferred(path string, d Deferred) error {
	f, err := os.Create(path) //nolint:gosec // the file the user names
	if err != nil {
		return err
	}

	return errors.Join(json.NewEncoder(f).Encode(d), f.Close())
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
// mutants are found again in the module's source by position and type, and their tests get the
// deferred time. It answers the mutants, or an error when one is no longer in the source.
func (m *MutantExecutorDealer) Resume(ctx context.Context, d Deferred) ([]mutator.Mutator, error) {
	m.testExecutionTime = d.Timeout
	files := map[string]*parsedFile{}
	muts := make([]mutator.Mutator, 0, len(d.Mutants))
	for _, dm := range d.Mutants {
		mut, err := m.find(files, dm)
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
		m.selector.Disable(pkg, errors.New("the run that deferred its mutants disabled it"))
	}
	m.Settle(ctx, muts)

	return muts, nil
}

// outcomeNamed is the outcome againNames names name, and whether there is one.
func outcomeNamed(name string) (outcome, bool) {
	for o, n := range againNames {
		if n == name {
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

// find is the mutant dm names, in its file of the module's source.
func (m *MutantExecutorDealer) find(files map[string]*parsedFile, dm DeferredMutant) (mutator.Mutator, error) {
	pf, ok := files[dm.File]
	if !ok {
		set := token.NewFileSet()
		src, err := os.ReadFile(filepath.Join(m.mod.Root, m.mod.CallingDir, filepath.FromSlash(dm.File)))
		if err != nil {
			return nil, err
		}
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
