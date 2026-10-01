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
- `NOT VIABLE`: The compiler refuses the mutated line. It is reported, and counts neither as killed nor as lived.
- `UNCLASSIFIED`: No verdict was reached. It never counts as killed.

Gremlins runs each mutation's tests with `go test -json -vet=off`: vet refuses some mutations that build (an
`INVERT_LOGICAL` of `x == a || x == b` is a suspect and to vet), and the tests must run on them. It reads each run
from the run's events. A test that fails, a panic inside a test included, kills the mutation. A build failure is
`NOT VIABLE` only when the compiler names the mutated line of the mutated package; any other build failure, of
another package or before the build started, is `UNCLASSIFIED`. A package whose tests fail with no test failing (a
signal that ended the test binary, a panic outside a test, `TestMain` exiting) is a crash. A time-out or a crash
runs again alone once every other mutation has run, with every test of its package and the same timeout, since the
load of the other runs may have caused it. A time-out that repeats is `TIMED OUT` and a crash that repeats is
`KILLED`, but only if every test then passes on the unmutated code in the same place: otherwise the place, not the
mutation, failed, and the mutation is `UNCLASSIFIED`. A test failure or a pass alone is the verdict, and anything
else is `UNCLASSIFIED`. A run that could not be carried out (the go command did not start, refused its flags with
exit status 2, or failed before it ran the package's tests) is `UNCLASSIFIED` at once.

The events are those the go command writes; a test can still write a line that `go test -json` reads as a test's
failure (test2json's framing), so Gremlins' verdicts assume the tests do not imitate the test framework's output.

When a mutation cannot be undone in a worker's copy of the module, Gremlins replaces that copy with a fresh one and
checks the mutated file against the source; if it cannot, every later mutation of that worker is `UNCLASSIFIED`.
