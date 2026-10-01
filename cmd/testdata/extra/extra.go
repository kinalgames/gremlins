// Package extra has mutations Gremlins does not find or cannot see covered, which a list gives it
// as text replacements (cmd/testdata/extra.txt): a constant and a case of a tagless switch.
package extra

// Limit is the largest size that fits.
const Limit = 10

// Fits reports whether n fits.
func Fits(n int) bool {
	switch {
	case n <= Limit:
		return true
	}

	return false
}
