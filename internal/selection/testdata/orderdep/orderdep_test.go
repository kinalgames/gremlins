package orderdep

import "testing"

func TestFirst(t *testing.T) { Set(1) }

func TestSecond(t *testing.T) {
	if Get() != 1 {
		t.Fatal("TestFirst did not run before")
	}
}
