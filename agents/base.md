# Environment

You are running in a sandboxed container with no access to the host.

- Docker is NOT available. You cannot run containers or `docker compose`.
  For justfiles use `just --evaluate` and `just --dry-run <recipe>`; for YAML use `yq`.
- Tools: git, gh, jq, yq, just, rg, fd, shellcheck, hadolint, actionlint.
  Lint what you change: `shellcheck` for shell, `hadolint` for Dockerfiles,
  `actionlint` for workflows.

# Design choices

- Don't claim design intent you weren't told. If you're unsure whether behavior is
  intended, describe what the code does and list it under "Open questions" in your
  summary and PR description. Never write comments asserting intent you inferred.

# Git workflow

- Never commit to main. Create a branch, push it, and open a PR with `gh pr create`.
- Commit messages and PR titles use Conventional Commits: `type: subject`,
  types: feat, fix, minor, config, infra, ci, doc, spelling, dependency.
  Use `!` for breaking changes (e.g. `feat!: ...`).
- Once a PR is open, add new commits for changes; don't amend or force-push.
- If a push or command is rejected, stop and report it. Don't try workarounds.
- Never modify `.github/workflows/` or other CI configuration. If a CI check fails
  because of its own rules (not your code), stop and report what the check requires.


