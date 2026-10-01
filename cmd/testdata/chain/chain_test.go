package chain

import "testing"

func TestJoin(t *testing.T) {
	if Join("a", "b", "c") != "abc" {
		t.Fatal("Join")
	}
}
