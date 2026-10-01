package escape

import (
	"os"
	"testing"
)

// TestMain ends the test binary the first time A is not n plus one, so that A's mutant crashes
// once and runs again alone: the file ESCAPE_MARKER names records the crash.
func TestMain(m *testing.M) {
	if marker := os.Getenv("ESCAPE_MARKER"); marker != "" && A(1) != 2 {
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			os.Exit(3)
		}
	}
	os.Exit(m.Run())
}

func TestA(t *testing.T) {
	if !Check(A(1)) {
		t.Fatal("Check refuses A(1)")
	}
}
