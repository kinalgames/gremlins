package dirty

import (
	"os"
	"testing"
)

func TestA(t *testing.T) {
	Shift(1)
	if Scale(3) != 6 {
		_ = os.Chmod("a.go", 0o444)
		t.Fatal("Scale")
	}
}
