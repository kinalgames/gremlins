// Package relrep depends on a module its go.mod replaces with a directory beside it, which the
// source has and the copies a mutant runs in do not. TestAdd checks Add; no test checks Sub.
package relrep

import "example.com/ext"

// Add adds a and b.
func Add(a, b int) int { return a + b + ext.Zero }

// Sub subtracts b from a.
func Sub(a, b int) int { return a - b }
