// Package dirty has a test that, when it sees a mutant, makes the mutated file read-only, as a file
// lock or a full disk can keep a mutant from being undone. TestA checks Scale; no test checks Shift.
package dirty

// Scale doubles x.
func Scale(x int) int { return x * 2 }
