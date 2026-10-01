// Package pollute keeps a registry that a test leaves filled for the tests after it.
package pollute

import "errors"

var registry = map[string]int{}

// Register keeps name with the scaled n, and refuses a name it keeps already.
func Register(name string, n int) error {
	if _, dup := registry[name]; dup {
		return errors.New("duplicate")
	}
	registry[name] = Scale(n)

	return nil
}

// Reset empties the registry.
func Reset() { registry = map[string]int{} }

// Scale is what a registration keeps; no test checks it.
func Scale(n int) int {
	return n * 2
}
