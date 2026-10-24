# Toolchain: Rust

- Use `cargo` for everything; don't invoke `rustc` directly or hand-roll build steps.
- Toolchain is managed by `rustup`; the default toolchain is pinned via `RUST_VERSION` in the
  Dockerfile. Prefer a repo's `rust-toolchain.toml` when present; don't change the pinned
  version unless asked.
- Before committing:
  - `cargo fmt --check` (run `cargo fmt` to fix)
  - `cargo clippy --all-targets -- -D warnings`
  - `cargo test`
- No separate environment directory is needed; cargo manages its own target dir and caches.
- To run a server for manual checks: start it in the background with output to a log,
  record its PID, and stop it by that PID when done: for example
  `cargo run > /tmp/server.log 2>&1 & echo $! > /tmp/server.pid`
  then `kill "$(cat /tmp/server.pid)"`. Only kill processes you started.
- Use a fixed port from the repo's config or tests; don't scan for ports.
