FROM node:22-bookworm

# add whatever toolchains your repos need: go, python, etc.
RUN apt-get update \
    && apt-get install -y git ripgrep curl wget \
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
