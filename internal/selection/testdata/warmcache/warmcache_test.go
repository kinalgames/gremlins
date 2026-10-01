package warmcache

import "testing"

// TestPrime fills the cache for 3.
func TestPrime(*testing.T) { Double(3) }

// TestCheck reads the cache for 3 only after TestPrime.
func TestCheck(t *testing.T) {
	if Double(3) != 6 {
		t.Fatal("Double(3)")
	}
}

// TestTwice reads the cache and checks nothing.
func TestTwice(*testing.T) {
	Double(5)
	Double(5)
}
