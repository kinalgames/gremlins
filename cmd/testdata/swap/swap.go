// Package swap has two mutants of one type at one column: A's crashes once and no test checks
// it, B's a test kills.
package swap

// A is n plus one.
func A(n int) int { return n + 1 }

// B is n plus two.
func B(n int) int { return n + 2 }
