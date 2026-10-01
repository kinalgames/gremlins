package relrep

import "testing"

func TestAdd(t *testing.T) {
	Sub(1, 2)
	if Add(1, 2) != 3 {
		t.Fatal("Add")
	}
}
