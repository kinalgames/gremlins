// Package envfault has two mutants that crash the first time their tests run: no test checks A's,
// and TestB kills B's when it runs again alone. TestEnv stands for a test that needs a shared
// service, which may go down between a run and the run that settles its mutants.
package envfault

// A is n plus one.
func A(n int) int { return n + 1 }

// B is n plus two.
func B(n int) int { return n + 2 }
