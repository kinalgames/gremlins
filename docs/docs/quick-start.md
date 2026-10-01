# Quick start

To execute a mutation test run, from the root of a Go module execute:

```sh
$ gremlins unleash #(1)
```

1. If `unleash` is too long to type for you, you can use `run` or `r` which will do the same.

Gremlins only tests mutations of parts of the code already covered by test cases. If a mutant is not covered, why bother
testing? You already know it will not be caught. In any case, Gremlins will report which mutations aren't covered.

Gremlins will report each mutation as:

- `RUNNABLE`: In _dry-run_ mode, a mutation that can be tested.
- `NOT COVERED`: A mutation not covered by tests; it will not be tested.
- `KILLED`: The mutation has been caught by the test suite.
- `LIVED`: The mutation hasn't been caught by the test suite.
- `TIMED OUT`: The tests timed out while testing the mutation, again when the mutation ran alone: the mutation
  actually made the tests fail, but not explicitly. It counts as killed.
- `NOT VIABLE`: The mutated code does not build. It is reported, and counts neither as killed nor as lived.
- `UNCLASSIFIED`: No verdict was reached. It never counts as killed.

Gremlins reads each run from the events of `go test -json`. A test that fails, a panic inside a test included, kills
the mutation. A package that fails with no test failing (a signal that ended the test binary, a panic outside a test,
`TestMain` exiting) is a crash. A time-out or a crash runs again alone once every other mutation has run, with every
test of its package and the same timeout, since the load of the other runs may have caused it: a time-out that
repeats is `TIMED OUT`, a crash that repeats is `KILLED`, a test failure, a pass or a build failure alone is the
verdict, and anything else is `UNCLASSIFIED`. A run that could not be carried out (the go command did not start, or
refused its flags with exit status 2) is `UNCLASSIFIED` at once.
