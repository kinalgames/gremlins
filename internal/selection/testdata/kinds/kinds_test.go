package kinds

import (
	"fmt"
	"os"
	"testing"
)

// TestMain prints a line that starts as a test's name does.
func TestMain(m *testing.M) {
	fmt.Println("TestMain is ready")
	os.Exit(m.Run())
}

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add")
	}
}

func TestAddMore(t *testing.T) {
	if More(2) != 20 {
		t.Fatal("More")
	}
}

func TestHalf(t *testing.T) {
	t.Run("even", func(t *testing.T) {
		if Half(4) != 2 {
			t.Fatal("Half(4)")
		}
	})
	t.Run("odd", func(t *testing.T) {
		if Half(5) != 2 {
			t.Fatal("Half(5)")
		}
	})
}

func FuzzSize(f *testing.F) {
	f.Add("ab")
	f.Fuzz(func(t *testing.T, s string) {
		if Size(s) != len(s)-1 {
			t.Fatal("Size")
		}
	})
}
