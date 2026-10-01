/*
 * Copyright 2022 The Gremlins Authors
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

// Package engine orchestrates mutation testing by discovering, applying, and testing mutations.
package engine

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gremlins/gremlins/internal/coverage"
	"github.com/go-gremlins/gremlins/internal/diff"
	"github.com/go-gremlins/gremlins/internal/engine/workerpool"
	"github.com/go-gremlins/gremlins/internal/exclusion"
	"github.com/go-gremlins/gremlins/internal/log"
	"github.com/go-gremlins/gremlins/internal/mutator"
	"github.com/go-gremlins/gremlins/internal/report"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/gomodule"
)

// Engine is the "engine" that performs the mutation testing.
//
// It traverses the AST of the project, finds which TokenMutator can be applied and
// performs the actual mutation testing.
type Engine struct {
	fs           fs.FS
	jDealer      ExecutorDealer
	codeData     CodeData
	mutantStream chan mutator.Mutator
	module       gomodule.GoModule
	logger       report.MutantLogger
	extras       []mutator.Mutator
}

// CodeData is used to check if the mutant should be executed.
type CodeData struct {
	Cov       coverage.Profile
	Diff      diff.Diff
	Exclusion exclusion.Rules
}

// Option for the Engine initialization.
type Option func(m Engine) Engine

// New instantiates an Engine.
//
// It gets a fs.FS on which to perform the analysis, a CodeData to
// check if the mutants are executable and a sets of Option.
func New(mod gomodule.GoModule, codeData CodeData, jDealer ExecutorDealer, opts ...Option) Engine {
	dirFS := os.DirFS(filepath.Join(mod.Root, mod.CallingDir))
	mut := Engine{
		module:   mod,
		jDealer:  jDealer,
		codeData: codeData,
		fs:       dirFS,
		logger:   report.NewLogger(),
	}
	for _, opt := range opts {
		mut = opt(mut)
	}

	return mut
}

// WithDirFs overrides the fs.FS of the module (mainly used for testing purposes).
func WithDirFs(dirFS fs.FS) Option {
	return func(m Engine) Engine {
		m.fs = dirFS

		return m
	}
}

// WithExtraMutants adds the mutants the user wrote (ReadExtraMutants) to those the engine finds.
// They run whatever the coverage, the diff and the file exclusions say: the user chose them.
func WithExtraMutants(muts []mutator.Mutator) Option {
	return func(m Engine) Engine {
		m.extras = muts

		return m
	}
}

// Run executes the mutation testing.
//
// It walks the fs.FS provided and checks every .go file which is not a test.
// For each file it will scan for tokenMutations and gather all the mutants found.
func (mu *Engine) Run(ctx context.Context) report.Results {
	mu.mutantStream = make(chan mutator.Mutator)
	go func() {
		defer close(mu.mutantStream)
		_ = fs.WalkDir(mu.fs, ".", func(path string, _ fs.DirEntry, _ error) error {
			isGoCode := filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go")

			if isGoCode && !mu.codeData.Exclusion.IsFileExcluded(path) {
				mu.runOnFile(path)
			}

			return nil
		})
		for _, m := range mu.extras {
			mu.mutantStream <- m
		}
	}()

	start := time.Now()
	res := mu.executeTests(ctx)
	res.Elapsed = time.Since(start)
	res.Module = mu.module.Name

	return res
}

func (mu *Engine) runOnFile(fileName string) {
	src, _ := mu.fs.Open(fileName)
	set := token.NewFileSet()
	file, _ := parser.ParseFile(set, fileName, src, parser.ParseComments)
	_ = src.Close()

	ast.Inspect(file, func(node ast.Node) bool {
		n, ok := NewTokenNode(node)
		if !ok {
			return true
		}
		mu.findMutations(fileName, set, file, n)

		return true
	})
}

func (mu *Engine) findMutations(fileName string, set *token.FileSet, file *ast.File, node *NodeToken) {
	mutantTypes, ok := TokenMutantType[node.Tok()]
	if !ok {
		return
	}

	pkg := packageOf(mu.module, fileName)
	for _, mt := range mutantTypes {
		if !configuration.Get[bool](configuration.MutantTypeEnabledKey(mt)) {
			continue
		}
		mutantType := mt
		tm := NewTokenMutant(pkg, set, file, node)
		tm.SetType(mutantType)
		tm.SetStatus(mu.mutationStatus(set.Position(node.TokPos)))

		mu.mutantStream <- tm
	}
}

// packageOf is the import path of the package fileName (relative to mod's calling directory)
// belongs to: the module name plus the file's directory relative to the module root. The package
// clause is not consulted, because an import path never depends on the package name: walking up
// the directories for one ending in the package name sent every `package main` (and every package
// named unlike its directory) to the module root, whose tests then ran in place of the mutated
// package's and reported each mutant as killed.
func packageOf(mod gomodule.GoModule, fileName string) string {
	dir := filepath.Dir(filepath.Join(mod.CallingDir, fileName))
	if dir == "." {
		return mod.Name
	}

	return normalisePkgPath(mod.Name + "/" + dir)
}

func normalisePkgPath(pkg string) string {
	sep := fmt.Sprintf("%c", os.PathSeparator)

	return strings.ReplaceAll(pkg, sep, "/")
}

func (mu *Engine) mutationStatus(pos token.Position) mutator.Status {
	var status mutator.Status

	if mu.codeData.Cov.IsCovered(pos) {
		status = mutator.Runnable
	}

	if !mu.codeData.Diff.IsChanged(mu.fromModuleRoot(pos)) {
		status = mutator.Skipped
	}

	return status
}

// fromModuleRoot names pos's file from the module root, as the diff does: the engine reads the
// files of the directory it was started for by their names in that directory.
func (mu *Engine) fromModuleRoot(pos token.Position) token.Position {
	pos.Filename = filepath.ToSlash(filepath.Join(mu.module.CallingDir, pos.Filename))

	return pos
}

func (mu *Engine) executeTests(ctx context.Context) report.Results {
	pool := workerpool.Initialize("mutator")
	pool.Start()

	var mutants []mutator.Mutator
	outCh := make(chan mutator.Mutator)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for mut := range mu.mutantStream {
			ok := checkDone(ctx)
			if !ok {
				pool.Stop()

				break
			}
			wg.Add(1)
			pool.AppendExecutor(mu.jDealer.NewExecutor(mut, outCh, wg))
		}
	}()

	go func() {
		wg.Wait()
		close(outCh)
	}()

	settler, _ := mu.jDealer.(Settler)
	var pending []mutator.Mutator
	for m := range outCh {
		if settler != nil && settler.Pending(m) {
			pending = append(pending, m)

			continue
		}
		mu.logger.Mutant(m)
		mutants = append(mutants, m)
	}
	if len(pending) == 0 {
		return results(mutants)
	}
	if mu.deferSettle(pending) {
		res := results(mutants)
		res.Pending = len(pending)

		return res
	}
	settler.Settle(ctx, pending)
	for _, m := range pending {
		mu.logger.Mutant(m)
	}

	return results(append(mutants, pending...))
}

// Deferrer is a Settler that can hand its pending mutants to a later run (Deferred), or answers
// why it cannot.
type Deferrer interface {
	Defer(pending []mutator.Mutator) (Deferred, error)
}

// deferSettle writes the pending mutants to the file --settle-later names, for a later run with
// --settle to give them their verdicts, and reports whether it did: they are then left out of
// this run's results. Without the setting, or when the file cannot be written, this run settles
// them, and no file is left at the path.
func (mu *Engine) deferSettle(pending []mutator.Mutator) bool {
	path := configuration.Get[string](configuration.UnleashSettleLaterKey)
	deferrer, ok := mu.jDealer.(Deferrer)
	if path == "" || !ok {
		return false
	}
	d, err := deferrer.Defer(pending)
	if err == nil {
		err = WriteDeferred(path, d)
	}
	if err != nil {
		log.Errorf("failed to defer the runs alone to %s, so they run now\n\t%v\n", path, err)
		if err := RemoveDeferred(path); err != nil {
			log.Errorf("failed to remove %s, which a later run must not settle\n\t%v\n", path, err)
		}

		return false
	}
	pkg := filepath.Join(mu.module.Root, mu.module.CallingDir)
	if len(pending) == 1 {
		log.Infof("1 mutant waits to run alone: gremlins unleash --settle %s %s\n", path, pkg)
	} else {
		log.Infof("%d mutants wait to run alone: gremlins unleash --settle %s %s\n", len(pending), path, pkg)
	}

	return true
}

func checkDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

func results(m []mutator.Mutator) report.Results {
	return report.Results{Mutants: m}
}
