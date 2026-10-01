// Package kinds is reached by each kind of test a test binary lists.
package kinds

// Add is reached by TestAdd.
func Add(a, b int) int {
	return a + b
}

// More is reached by TestAddMore, whose name starts with TestAdd.
func More(a int) int {
	return a * 10
}

// Half is reached by the subtests of TestHalf.
func Half(n int) int {
	return n / 2
}

// Neg is reached by TestNeg, of the external test package.
func Neg(n int) int {
	return -n
}

// Size is reached by the seed corpus of FuzzSize.
func Size(s string) int {
	return len(s) - 1
}

// Tagged is reached by TestTagged, built with the tag extra only.
func Tagged(n int) int {
	return n % 7
}
