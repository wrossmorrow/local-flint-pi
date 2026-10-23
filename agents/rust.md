# Toolchain: Rust

- Use `cargo` for everything; don't invoke `rustc` directly or hand-roll build steps.
- Toolchain is managed by `rustup`; the default toolchain is pinned via `RUST_VERSION` in the
  Dockerfile. Prefer a repo's `rust-toolchain.toml` when present; don't change the pinned
  version unless asked.
- Before committing:
  - `cargo fmt --check .` (run `cargo fmt .` to fix)
  - `cargo clippy`
  - `cargo test`
- No separate environment directory is needed; cargo manages its own target dir and caches.
