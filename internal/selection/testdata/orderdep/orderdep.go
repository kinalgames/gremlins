// Package orderdep has a test that passes only after another.
package orderdep

var state int

// Set sets the state.
func Set(n int) { state = n }

// Get gets it.
func Get() int { return state }
