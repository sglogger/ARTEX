# Test layout and how to run

Tests are filed under the package they belong to; there is no central directory mixing all modules:

```text
agent/tests/*_test.go
cmd/artex/tests/*_test.go
db/tests/*_test.go
server/tests/*_test.go
norma/llm/tests/*_test.go
web/src/lib/tests/*.test.mjs
tests/                      # repository-level test entry point and release script tests
```

## Go tests

Go treats subdirectories as separate packages, while the existing white-box tests depend on unexported members of their own package. So the test sources use `//go:build ignore` to avoid being compiled as subpackages; `run.go` strips that header in a temporary directory and generates a native Go `-overlay` mapping that maps the tests **virtually** back into the original package.

No business code is moved, no internal API is exported and no files are copied into business directories. Package identity, TestMain, relative fixture paths and coverage all keep the original package semantics. Temporary sources and the overlay are cleaned up after the run. **Running `go test ./...` directly does not load the relocated tests; you must use the entry point below.**

Run from the repository root:

```bash
go run ./tests/run.go                          # ARTEX main module
go run ./tests/run.go --module norma           # standalone norma module
go run ./tests/run.go --all                    # both modules; if one fails the other is still checked
go run ./tests/run.go ./config ./notify -count=1
go run ./tests/run.go -race ./db ./server -run TestSide -count=1
go run ./tests/run.go --all -run '^$'           # compile all tests without running test cases
go test ./tests/run.go ./tests/run_test.go      # unit / integration tests of the entry point itself
```

`--all` / `--module` go first among the entry point's arguments, and the remaining arguments are passed to `go test`; with no package given the default is `./...`. When running across both modules, package arguments must exist in both modules (usually the default `./...`). Go's `-overlay` flag is managed by the entry point and cannot be overridden separately.

New Go tests go into the owning package's `tests/<file name>_test.go`, still declaring the original package name, and always start with:

```go
//go:build ignore

package yourpackage
```

Test fixtures stay in the original package's `testdata`; the working directory is still the original package directory. The entry point isolates nested modules and ignores node_modules/vendor and build directories; it does not scan frontend tests or skill reference docs.

Tests that depend on PostgreSQL need `ARTEX_PG_DSN` pointing at a **disposable test database**. Never use a production database. Without a configured database some existing integration tests skip and some fail; the Windows environment and norma also have existing failing tests, and this migration did not change assertions to hide those problems.

## Frontend and release scripts

```bash
cd web
npm test                         # Node.js 22.6+, no extra test framework needed
cd ..
python tests/build_test.py        # Bash + Python 3.9+; Git Bash is used automatically on Windows
```

The release script test runs `build.sh` in an isolated temporary directory, simulating the Go build but actually creating the zips, verifying SHA256 and the startup scripts' permissions. It verifies all five platforms, the exclusion of old zips, early failure on unsupported / duplicate targets, and that checksum tool failures cannot be ignored; it does not mean a real frontend build or an actual release has been done.

Before branch pushes, PRs and Releases, CI runs the entry point tests, the release script test, a compile check of all Go tests, regression of the config/notify/selfupdate core packages and the frontend unit tests. The compile check is not a claim that the full regression passes; the full regression still uses the main module / norma / `--all` commands above.
