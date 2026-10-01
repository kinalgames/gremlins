package residual

import "testing"

func TestDouble(t *testing.T) {
	if Double(2) != 4 {
		t.Fatal("Double")
	}
}

// TestA adds to the total and checks nothing.
func TestA(*testing.T) { Add(5) }

// TestB empties the total for the tests after it.
func TestB(*testing.T) { Reset() }

// TestC fails on a negative total, which only a mutant of Add leaves.
func TestC(t *testing.T) {
	if Add(0) < 0 {
		t.Fatal("negative total")
	}
}
