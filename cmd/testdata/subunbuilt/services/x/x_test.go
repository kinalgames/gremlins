package x

import "testing"

func TestX(t *testing.T) {
	if Shout("a") != "a!" || Join("a", "b", "c") != "abc" {
		t.Fatal("x")
	}
}
