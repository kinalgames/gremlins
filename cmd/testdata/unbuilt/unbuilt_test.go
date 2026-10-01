package unbuilt

import "testing"

func TestGreet(t *testing.T) {
	if Greet("x") != "hi x" {
		t.Fatal("Greet")
	}
}
