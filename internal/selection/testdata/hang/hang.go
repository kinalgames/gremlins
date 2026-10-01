// Package hang holds a resource that a test leaves taken for the tests after it.
package hang

import "time"

var busy bool

// Take waits for the resource and takes it.
func Take() {
	for busy {
		time.Sleep(time.Millisecond)
	}
	busy = true
}

// Free frees the resource.
func Free() { busy = false }

// Scale is reached by TestA and TestC; no test checks it.
func Scale(n int) int {
	return n * 2
}
