
Wrappers for running [recursive's flint](https://huggingface.co/RecursiveSI/flint) with [pi harness](https://pi.dev/). 

The goal is largely to encapsulate work away from other files (particularly sensitive ones) as well as local copies of a repo. A GitHub app is used to get tokens for interacting with `git` with external permission controls. Flint runs locally, but the agent and its repo copy runs in a container.

The agent base image now ships with Python (with [uv](https://docs.astral.sh/uv/) as the package manager) and Go toolchains. Add or extend the `Dockerfile` as needed for further languages.
