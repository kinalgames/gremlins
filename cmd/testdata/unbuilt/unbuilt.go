// Package unbuilt has a mutant that does not build: a string cannot be subtracted.
package unbuilt

// Greet greets name.
func Greet(name string) string {
	return "hi " + name
}
