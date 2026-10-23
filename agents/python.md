# Toolchain: Python (uv)

- Use `uv` for everything; do not use `pip`, `pip install`, or `python -m venv`.
- Setup: `uv sync` (creates `.venv` from `pyproject.toml` and `uv.lock`).
- Run anything through uv, e.g. `uv run pytest`, `uv run python script.py`. No need to activate the venv.
- Dependencies: `uv add <pkg>` (or `uv add --dev <pkg>` for test/lint tools). Never edit
  `uv.lock` by hand; commit it along with `pyproject.toml`.
- Before committing:
  - `uv run ruff format --check .` (run `uv run ruff format .` to fix)
  - `uv run ruff check .`
  - `uv run pytest`
  - type check only if the repo configures one: `uv run mypy .` or `uv run pyright`
- If ruff/pytest aren't project dependencies, use `uvx ruff ...` / `uvx pytest` rather than
  adding them, and note in your final summary and the PR description that they should be
  added as dev dependencies (`uv add --dev ruff pytest`).
- Don't change the Python version (`.python-version`, `requires-python`) unless asked.
