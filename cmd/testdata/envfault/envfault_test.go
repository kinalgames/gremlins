package envfault

import (
	"os"
	"testing"
)

// TestMain ends the test binary the first time A or B is mutated, so that each mutant crashes once:
// the file ENVFAULT_MARKER names, followed by the function's name, records the crash.
func TestMain(m *testing.M) {
	marker := os.Getenv("ENVFAULT_MARKER")
	for name, mutated := range map[string]bool{"A": A(1) != 2, "B": B(1) != 3} {
		if _, err := os.Stat(marker + name); marker != "" && mutated && err != nil {
			_ = os.WriteFile(marker+name, nil, 0o600)
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

// TestEnv fails while the file ENVFAULT_DOWN names exists: the shared service is down.
func TestEnv(t *testing.T) {
	if down := os.Getenv("ENVFAULT_DOWN"); down != "" {
		if _, err := os.Stat(down); err == nil {
			t.Fatal("the shared service is down")
		}
	}
}
