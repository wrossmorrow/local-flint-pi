
Wrappers for running [recursive's flint](https://huggingface.co/RecursiveSI/flint) with [pi harness](https://pi.dev/). 

The goal is largely to encapsulate work away from other files (particularly sensitive ones) as well as local copies of a repo. A GitHub app is used to get tokens for interacting with `git` with external permission controls. Flint runs locally, but the agent and its repo copy runs in a container.

The agent base image now ships with Python (with [uv](https://docs.astral.sh/uv/) as the package manager), the Go toolchain, Java (17 and 21), and the [just](https://github.com/casey/just) task runner. Toolchain-specific images are built as derived stages of the same `Dockerfile`:

- `agent` (default): the full agent image
- `python`: adds the Python + uv toolchain on top of the agent image
- `golang`: adds the Go toolchain on top of the agent image
- `rust`: adds the Rust toolchain on top of the agent image
- `java17`: adds the JDK 17 toolchain on top of the agent image
- `java21`: adds the JDK 21 toolchain on top of the agent image

Build a toolchain-specific image with `just build <toolchain>`, or directly via `docker build --target <toolchain> -t <image> .`.
