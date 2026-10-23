ARG NODE_TAG=22-bookworm
FROM node:${NODE_TAG} AS agent

# add whatever toolchains your repos need: go, python, etc.
RUN apt-get update \
    && apt-get install -y git ripgrep curl wget fd-find \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/*

ARG GH_VERSION=2.62.0
ARG TARGETARCH
RUN curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    | tar -xz -C /tmp \
    && mv /tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh /usr/local/bin/gh \
    && rm -rf /tmp/gh_*

# TODO: pin a version if messy
RUN npm install -g @earendil-works/pi-coding-agent

COPY --chmod=u=rwX,go=rX rec-ext /opt/rec
COPY --chmod=755 entrypoint.sh /opt/entrypoint.sh

RUN mkdir -p /home/node/.pi /work \
    && chown node:node /home/node/.pi /work

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# Python toolchain: derives from the agent image and adds the Python package
# manager uv on top.
FROM agent AS python
USER root
RUN apt-get update \
    && apt-get install -y python3 python3-pip \
    && rm -rf /var/lib/apt/lists/*

ENV UV_INSTALL_DIR=/usr/local/uv
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
    && ln -s /usr/local/uv/bin/uv /usr/local/bin/uv \
    && ln -s /usr/local/uv/bin/uvx /usr/local/bin/uvx

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# Go toolchain: derives from the agent image and adds the Go toolchain on top.
FROM agent AS golang
USER root
ARG GO_VERSION=1.23.4
ARG TARGETARCH
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]
