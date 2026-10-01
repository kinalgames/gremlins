package testedit

import (
	"os"
	"testing"
)

// TestMain ends the test binary the first time A is not n plus one, so that A's mutant crashes
// once and passes when it runs again: the file TESTEDIT_MARKER names records the crash.
func TestMain(m *testing.M) {
	if marker := os.Getenv("TESTEDIT_MARKER"); marker != "" && A(1) != 2 {
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, nil, 0o600)
			os.Exit(3)
		}
	}
	os.Exit(m.Run())
}
