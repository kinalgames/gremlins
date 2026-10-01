//go:build extra

package kinds

import "testing"

func TestTagged(t *testing.T) {
	if Tagged(8) != 1 {
		t.Fatal("Tagged")
	}
}
