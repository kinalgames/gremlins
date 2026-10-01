package kinds_test

import (
	"testing"

	"example.com/kinds"
)

func TestNeg(t *testing.T) {
	if kinds.Neg(2) != -2 {
		t.Fatal("Neg")
	}
}
