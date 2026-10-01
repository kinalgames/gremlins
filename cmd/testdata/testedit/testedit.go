// Package testedit has one mutant: it crashes once and no test checks it, so its verdict rests on
// the tests a later edit of the test file could add.
package testedit

// A is n plus one.
func A(n int) int { return n + 1 }
