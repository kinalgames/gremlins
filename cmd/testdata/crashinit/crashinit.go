// Package crashinit has mutants that make the package's initialisation panic, outside every test:
// crashes, which repeat when each mutant runs alone, on code whose tests pass unmutated.
package crashinit

func init() {
	if size := 2 - 1; size != 1 {
		panic("size")
	}
}
