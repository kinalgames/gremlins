package swap

import (
	"os"
	"testing"
)

// TestMain ends the test binary the first time A is not n plus one, so that A's mutant crashes
// once and passes when it runs again: the file SWAP_MARKER names records the crash.
func TestMain(m *testing.M) {
	if marker := os.Getenv("SWAP_MARKER"); marker != "" && A(1) != 2 {
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			os.Exit(3)
		}
	}
	os.Exit(m.Run())
}

func TestB(t *testing.T) {
	if B(1) != 3 {
		t.Fatal("B(1) must be 3")
	}
}
