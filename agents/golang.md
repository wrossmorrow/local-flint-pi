# Toolchain: Go

- Before committing: `gofmt -l .` (must print nothing), `go vet ./...`,
  `go test -race -shuffle=on -count=1 ./...`
- Use `golangci-lint run` if the repo has a config for it.
- To run a server for manual checks: start it in the background with output to a log,
  record its PID, and stop it by that PID when done: for example
  `go run ./... > /tmp/server.log 2>&1 & echo $! > /tmp/server.pid`
  then `kill "$(cat /tmp/server.pid)"`. Only kill processes you started.
- Use a fixed port from the repo's config or tests; don't scan for ports.
