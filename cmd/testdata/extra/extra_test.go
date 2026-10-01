package extra

import (
	"os"
	"testing"
)

// TestMain ends the test binary when the limit changed: the mutant of the constant crashes.
func TestMain(m *testing.M) {
	if Limit != 10 {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func TestFits(t *testing.T) {
	if !Fits(10) || Fits(11) {
		t.Fatal("10 must fit and 11 must not")
	}
}
