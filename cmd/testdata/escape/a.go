// Package escape has one mutant: it crashes once, and the test that would kill it passes while
// Check accepts every value.
package escape

// A is n plus one.
func A(n int) int { return n + 1 }
