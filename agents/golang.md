# Toolchain: Go

- - Before committing: `gofmt -l .` (must print nothing), `go vet ./...`,
  `go test -race -shuffle=on -count=1 ./...`
- Use `golangci-lint run` if the repo has a config for it.
