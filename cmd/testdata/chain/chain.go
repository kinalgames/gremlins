// Package chain has mutants on an expression over three lines: the compiler names the line where
// the left operand starts, not the mutated operator's.
package chain

// Join joins a, b and c.
func Join(a, b, c string) string {
	return a +
		b +
		c
}
