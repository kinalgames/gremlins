// Package vetlive has a mutant that builds but that go vet refuses: INVERT_LOGICAL turns the
// comparison below into `r == ' ' && r == '\t'`, a suspect and. No test checks Blank's result.
package vetlive

// Blank reports whether r is a space or a tab.
func Blank(r rune) bool { return r == ' ' || r == '\t' }
