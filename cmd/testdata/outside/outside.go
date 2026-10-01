// Package outside has tests that need a file beside the module, which the source has and the
// copies a mutant runs in do not, as a database can be up for the coverage run and down later.
// TestScale checks Scale; no test checks Shift.
package outside

// Scale doubles x.
func Scale(x int) int { return x * 2 }

// Shift adds one to x.
func Shift(x int) int { return x + 1 }
