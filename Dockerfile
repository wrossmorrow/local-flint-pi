ARG TARGETARCH

ARG NODE_TAG=22-bookworm
ARG JUST_VERSION=1.59.0
ARG GH_VERSION=2.62.0

ARG GO_VERSION=1.26.10
ARG RUST_VERSION=1.99.1

ARG PROTOC_VERSION=36.2
ARG BUF_VERSION=1.75.0
ARG PROTOC_GEN_GO_VERSION=1.36.12
ARG PROTOC_GEN_GO_GRPC_VERSION=1.6.2

ARG GRADLE_VERSION=8.0.2

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Shell stage to ref buf version with an ARG
FROM bufbuild/buf:${BUF_VERSION} AS buf

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Build the pi web interface binary (Go, AWS SDK for S3 session uploads) that
# spawns `pi --mode rpc` and serves the browser UI. Baked into the base agent
# image below so the web interface and every toolchain derived from it ship with
# the agent.
FROM golang:1.26-alpine AS web-build
WORKDIR /src
COPY web/ ./
RUN go build -o /web .

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# main agent base container stage
FROM node:${NODE_TAG} AS agent

# git short sha of the source tree the pi web interface binary was built from;
# surfaced to the server as PI_WEB_BUILD_SHA (see GET /api/info)
ARG BUILD_SHA=unknown
ENV PI_WEB_BUILD_SHA=$BUILD_SHA

ENV PAGER=cat GIT_PAGER=cat LESS=-FRX GIT_TERMINAL_PROMPT=0 DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG PROTOC_VERSION

# add whatever toolchains your repos need: go, rust, python, java, etc.
RUN apt-get update \
    && apt-get install -y git ripgrep curl wget fd-find unzip \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/* \
    && case "${TARGETARCH}" in \
        arm64) PARCH=aarch_64 ;; amd64) PARCH=x86_64 ;; \
        *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/protoc.zip \
    "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PARCH}.zip" \
    && unzip -q /tmp/protoc.zip -d /usr/local bin/protoc 'include/*' \
    && chmod 755 /usr/local/bin/protoc \
    && rm /tmp/protoc.zip

COPY --from=buf /usr/local/bin/buf /usr/local/bin/buf

# just: task runner
ARG JUST_VERSION
RUN curl -fsSL "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-x86_64-unknown-linux-musl.tar.gz" -o /tmp/just.tar.gz \
    && tar -xzf /tmp/just.tar.gz -C /tmp \
    && mv /tmp/just /usr/local/bin/just \
    && rm -rf /tmp/just.tar.gz

COPY --from=koalaman/shellcheck:stable /bin/shellcheck /usr/local/bin/
COPY --from=hadolint/hadolint:latest   /bin/hadolint   /usr/local/bin/
COPY --from=rhysd/actionlint:latest    /usr/local/bin/actionlint /usr/local/bin/
COPY --from=mikefarah/yq:4             /usr/bin/yq     /usr/local/bin/

ARG GH_VERSION
RUN mkdir -p /usr/local/lib/gh \
    && curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
        | tar -xz -C /tmp \
    && mv /tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh /usr/local/lib/gh/gh \
    && rm -rf /tmp/gh_*

COPY --chmod=755 github-app/gh-shim.sh /usr/local/bin/gh

# TODO: pin a version if messy
RUN npm install -g @earendil-works/pi-coding-agent

COPY --chmod=u=rwX,go=rX rec-ext /opt/rec
COPY --chmod=755 entrypoint.sh /opt/entrypoint.sh

RUN mkdir -p /home/node/.pi /opt/agents /work \
    && chown node:node /home/node/.pi /work

COPY --chmod=644 agents/base.md /opt/agents/00-base.md

# Bake the pi web interface binary (built in the `web-build` stage above) into the
# base agent image, so the web interface and any toolchain derived from this image
# can run the agent via the RPC server.
COPY --from=web-build /web /usr/local/bin/pi-web

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Python toolchain: derives from the agent image and adds the Python package
# manager uv on top.
FROM agent AS python
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update \
    && apt-get install -y python3 python3-pip python-is-python3 \
    && rm -rf /var/lib/apt/lists/*

ENV UV_INSTALL_DIR=/usr/local/uv
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
    && ln -s /usr/local/uv/uv /usr/local/bin/uv \
    && ln -s /usr/local/uv/uvx /usr/local/bin/uvx

COPY --chmod=644 agents/python.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Go toolchain: derives from the agent image and adds the Go toolchain on top.
FROM agent AS golang
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG GO_VERSION
ARG PROTOC_GEN_GO_VERSION
ARG PROTOC_GEN_GO_GRPC_VERSION

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

RUN GOBIN=/usr/local/bin go install google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION} \
    && GOBIN=/usr/local/bin go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION} \
    && rm -rf /root/go /root/.cache/go-build

COPY --chmod=644 agents/golang.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Rust toolchain: derives from the agent image and adds the Rust toolchain on top.
FROM agent AS rust
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG RUST_VERSION

ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:/usr/local/rustup/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain ${RUST_VERSION} --profile minimal \
    && rustup component add rustfmt clippy \
    && chown -R node:node /usr/local/cargo /usr/local/rustup

ENV PROTOC=/usr/local/bin/protoc

COPY --chmod=644 agents/rust.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Java 21 toolchain: derives from the agent image and adds the JDK 21 on top.
FROM agent AS java21
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG JAVA_VERSION=21
ARG GRADLE_VERSION

RUN case "${TARGETARCH}" in \
      amd64) export JDK_ARCH=x64 ;; \
      arm64) export JDK_ARCH=aarch64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL "https://api.adoptium.net/v3/binary/latest/${JAVA_VERSION}/ga/linux/${JDK_ARCH}/jdk/hotspot/normal/eclipse" \
        -o /tmp/jdk.tar.gz \
    && mkdir -p /opt/jdk \
    && tar -xzf /tmp/jdk.tar.gz -C /opt/jdk --strip-components=1 \
    && rm -rf /tmp/jdk.tar.gz

ENV JAVA_HOME=/opt/jdk
ENV PATH="/opt/jdk/bin:${PATH}"

# gradle: binary distribution for JVM-based repos (e.g. envoy-extproc-sdk-java);
# installed locally so the gradle wrapper can resolve a distribution without
# relying solely on the network at first run
RUN curl -fsSL "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" -o /tmp/gradle.zip \
    && unzip -q /tmp/gradle.zip -d /opt \
    && ln -s /opt/gradle-${GRADLE_VERSION}/bin/gradle /usr/local/bin/gradle \
    && rm -f /tmp/gradle.zip

ENV GRADLE_HOME=/opt/gradle-${GRADLE_VERSION}
ENV PATH="/opt/gradle-${GRADLE_VERSION}/bin:${PATH}"

COPY --chmod=644 agents/java21.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Java 17 toolchain: derives from the agent image and adds the JDK 17 on top.
FROM agent AS java17
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG JAVA_VERSION=17
ARG GRADLE_VERSION

RUN case "${TARGETARCH}" in \
      amd64) export JDK_ARCH=x64 ;; \
      arm64) export JDK_ARCH=aarch64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL "https://api.adoptium.net/v3/binary/latest/${JAVA_VERSION}/ga/linux/${JDK_ARCH}/jdk/hotspot/normal/eclipse" \
        -o /tmp/jdk.tar.gz \
    && mkdir -p /opt/jdk \
    && tar -xzf /tmp/jdk.tar.gz -C /opt/jdk --strip-components=1 \
    && rm -rf /tmp/jdk.tar.gz

ENV JAVA_HOME=/opt/jdk
ENV PATH="/opt/jdk/bin:${PATH}"

# gradle: binary distribution for JVM-based repos (e.g. envoy-extproc-sdk-java);
# installed locally so the gradle wrapper can resolve a distribution without
# relying solely on the network at first run
RUN curl -fsSL "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" -o /tmp/gradle.zip \
    && unzip -q /tmp/gradle.zip -d /opt \
    && ln -s /opt/gradle-${GRADLE_VERSION}/bin/gradle /usr/local/bin/gradle \
    && rm -f /tmp/gradle.zip

ENV GRADLE_HOME=/opt/gradle-${GRADLE_VERSION}
ENV PATH="/opt/gradle-${GRADLE_VERSION}/bin:${PATH}"

COPY --chmod=644 agents/java17.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # # 

# Final stage: the full agent image (default build target for bare docker build).
# The pi web interface binary is baked into the base agent image, so this stage
# only re-declares the runtime user and entrypoint.
FROM agent AS final
USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]
