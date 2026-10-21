# Contributing

LAM is a Go 1.26 module. The CLI entry point lives in `cmd/lam`; application code and
its tests live in `internal/app`. Keep process and terminal integration tests close to
the package they exercise.

Before opening a change, run:

```sh
make check
make build
```

`make check` verifies Go formatting, runs `go vet`, and runs the test suite. `make build`
also builds the pinned libghostty-vt dependency before compiling the executable. The first
dependency build downloads pinned sources and can take several minutes.

Add tests for behavior changes and update the README when user-facing commands or
keyboard controls change. Keep generated binaries, downloaded dependencies, and local
session data out of the repository.
