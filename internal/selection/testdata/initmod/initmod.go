// Package initmod has code that only its initialisation reaches, code that one test reaches and
// code no test reaches.
package initmod

// limit is set at initialisation: only package-level initialisation reaches double.
var limit = double(21)

// registered is set by init: only init reaches triple.
var registered int

func init() {
	registered = triple(1)
}

func double(n int) int {
	return n * 2
}

func triple(n int) int {
	return n * 3
}

// Add adds.
func Add(a, b int) int {
	return a + b
}

// Sub subtracts.
func Sub(a, b int) int {
	return a - b
}

// Unused is reached by no test.
func Unused(a int) int {
	return a + 1
}

// Limits are the values initialisation set.
func Limits() (int, int) {
	return limit, registered
}
