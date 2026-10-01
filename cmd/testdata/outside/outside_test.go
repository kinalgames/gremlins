package outside

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if _, err := os.Stat("../shared.txt"); err != nil {
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestScale(t *testing.T) {
	Shift(1)
	if Scale(3) != 6 {
		t.Fatal("Scale")
	}
}
