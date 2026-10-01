// Package residual keeps a total that a mutant leaves wrong for the tests after it.
package residual

var total int

// Double is twice n.
func Double(n int) int { return n * 2 }

// Add adds n to the total and answers it.
func Add(n int) int {
	total = total + n

	return total
}

// Reset empties the total.
func Reset() { total = 0 }
