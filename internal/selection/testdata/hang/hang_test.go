package hang

import "testing"

// TestA leaves the resource taken.
func TestA(*testing.T) {
	Take()
	Scale(1)
}

// TestB frees it for TestC.
func TestB(*testing.T) { Free() }

// TestC waits for the resource forever unless TestB ran after TestA.
func TestC(*testing.T) {
	Take()
	Free()
	Scale(2)
}
