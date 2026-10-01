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
- `NOT VIABLE`: The mutation makes the build fail.
- `UNCLASSIFIED`: No verdict was reached: the tests timed out, a signal ended the test binary, or the tests could not
  run, and it did not repeat when the mutation ran alone. It never counts as killed.

A mutation whose tests timed out, whose test binary a signal ended, or whose tests could not run, runs again alone
once every other mutation has run, with every test of its package and the same timeout: the load of the other runs
may have caused it. A time-out or a signal that repeats counts as killed (`TIMED OUT`, `KILLED`); a test failure, a
pass or a build failure alone is the verdict; anything else is `UNCLASSIFIED`. A panic the testing package reports
is a test failure: `KILLED`, with no run alone.
