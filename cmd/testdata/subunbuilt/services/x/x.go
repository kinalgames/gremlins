// Package x is a package below its module's root, run as a consumer runs one package.
package x

// Shout shouts a.
func Shout(a string) string { return a + "!" }

// Join joins a, b and c.
func Join(a, b, c string) string {
	return a +
		b +
		c
}
