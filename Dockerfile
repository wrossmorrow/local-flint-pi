ARG NODE_TAG=22-bookworm
FROM node:${NODE_TAG} AS agent

ENV PAGER=cat GIT_PAGER=cat LESS=-FRX GIT_TERMINAL_PROMPT=0 DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# add whatever toolchains your repos need: go, rust, python, java, etc.
RUN apt-get update \
    && apt-get install -y git ripgrep curl wget fd-find unzip \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/*

# just: task runner
ARG JUST_VERSION=1.59.0
RUN curl -fsSL "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-x86_64-unknown-linux-musl.tar.gz" -o /tmp/just.tar.gz \
    && tar -xzf /tmp/just.tar.gz -C /tmp \
    && mv /tmp/just /usr/local/bin/just \
    && rm -rf /tmp/just.tar.gz

COPY --from=koalaman/shellcheck:stable /bin/shellcheck /usr/local/bin/
COPY --from=hadolint/hadolint:latest   /bin/hadolint   /usr/local/bin/
COPY --from=rhysd/actionlint:latest    /usr/local/bin/actionlint /usr/local/bin/
COPY --from=mikefarah/yq:4             /usr/bin/yq     /usr/local/bin/

ARG GH_VERSION=2.62.0
ARG TARGETARCH
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

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

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

# Go toolchain: derives from the agent image and adds the Go toolchain on top.
FROM agent AS golang
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG GO_VERSION=1.23.4
ARG TARGETARCH
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

COPY --chmod=644 agents/golang.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]

# Rust toolchain: derives from the agent image and adds the Rust toolchain on top.
FROM agent AS rust
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG RUST_VERSION=1.99.1
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:/usr/local/rustup/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain ${RUST_VERSION} --profile minimal \
    && rustup component add rustfmt clippy \
    && chown -R node:node /usr/local/cargo /usr/local/rustup

COPY --chmod=644 agents/rust.md /opt/agents/10-toolchain.md

USER node
WORKDIR /work

# Java 21 toolchain: derives from the agent image and adds the JDK 21 on top.
FROM agent AS java21
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG JAVA_VERSION=21
ARG TARGETARCH
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
ARG GRADLE_VERSION=8.0.2
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

# Java 17 toolchain: derives from the agent image and adds the JDK 17 on top.
FROM agent AS java17
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ARG JAVA_VERSION=17
ARG TARGETARCH
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
ARG GRADLE_VERSION=8.0.2
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

# Final stage: the full agent image (default build target for bare docker build)
FROM agent AS final
USER node
WORKDIR /work

ENTRYPOINT ["/opt/entrypoint.sh"]
