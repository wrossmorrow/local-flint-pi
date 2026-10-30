# Environment

You are running in a sandboxed container with no access to the host.

- Docker is NOT available. You cannot run containers or `docker compose`.
  For justfiles use `just --evaluate` and `just --dry-run <recipe>`; for YAML use `yq`.
- Tools: git, gh, jq, yq, just, rg, fd, shellcheck, hadolint, actionlint.
  Lint what you change: `shellcheck` for shell, `hadolint` for Dockerfiles,
  `actionlint` for workflows.
- Don't add new frameworks, code generators, or protocols without asking. If a task seems 
  to need a new tool, propose it in your summary first.
- If a tool you need is missing from the image, report it rather than installing it ad hoc.
- Don't modify AGENTS.md or any agent instruction files unless explicitly asked.
  If you think an instruction should change, suggest it in your summary.
- Never use pkill/killall by name. Stop only processes you started, by the PID you saved.

# Usability/Verifiability

- Build, test, generate code and run only through a repo's `just` recipes. If a justfile
  or recipe is missing or broken, create, add or fix the recipe rather than running ad hoc 
  commands before committing and pushing for maintainer feedback.
- Every PR description should include "How to verify": exact `just` commands and expected 
  output.

# Design choices

- Don't claim design intent you weren't told. If you're unsure whether behavior is
  intended, describe what the code does and list it under "Open questions" in your
  summary and PR description. Never write comments asserting intent you inferred.
- When removing a test or assertion, say why and where that behavior is covered now,
  and move it if it’s not covered anywhere.

# Git workflow

- Never commit to `main`. Create a branch, push it, and open a PR with `gh pr create`.
- Start each new task on a fresh branch from an up-to-date `main`, named for that task.
  Never reuse a branch whose PR has been merged.
- Commit messages and PR titles use Conventional Commits: `type: subject`,
  types: feat, fix, minor, config, infra, ci, doc, spelling, dependency.
  Use `!` for breaking changes (e.g. `feat!: ...`).
- Once a PR is open, add new commits for changes; don't amend or force-push.
- To update a PR branch with changes from `main`, merge `main` into it; don't rebase.
- If a push or command is rejected, stop and report it. Don't try workarounds.
- Never modify `.github/workflows/` or other CI configuration. If a CI check fails
  because of its own rules (not your code), stop and report what the check requires.

# Protobuf

- `buf` and `protoc` are installed. If the repo has a `buf.yaml`, run `buf lint` and
  `buf format -d` (diff only) on changed `.proto` files before committing.
- Check API compatibility with `buf breaking --against '.git#branch=main'`; report any
  breaking change in the PR description rather than working around it.
- If the repo has no `buf.yaml`, don't add one and don't "fix" lint findings unless asked.
  You can report this is missing if using protobuf to the maintainer, they can add.
- Never rename, renumber, or remove fields in existing messages to satisfy a linter;
  field numbers and names are the wire and JSON contract.

