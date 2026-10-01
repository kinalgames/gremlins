package initmod

import (
	"fmt"
	"testing"
)

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add")
	}
}

func TestSub(t *testing.T) {
	if Sub(3, 2) != 1 {
		t.Fatal("Sub")
	}
}

func ExampleSub() {
	fmt.Println(Sub(5, 3))
	// Output: 2
}

func BenchmarkAdd(b *testing.B) {
	for range b.N {
		Add(1, 2)
	}
}
