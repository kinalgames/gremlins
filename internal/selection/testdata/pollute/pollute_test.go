package pollute

import "testing"

// TestA leaves "x" registered.
func TestA(t *testing.T) {
	if err := Register("x", 1); err != nil {
		t.Fatal(err)
	}
}

// TestB empties the registry for TestC.
func TestB(*testing.T) { Reset() }

// TestC passes only after TestB.
func TestC(t *testing.T) {
	if err := Register("x", 2); err != nil {
		t.Fatal(err)
	}
}
