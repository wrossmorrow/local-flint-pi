FROM node:22-bookworm

# add whatever toolchains your repos need: go, python, etc.
RUN apt-get update \
    && apt-get install -y git ripgrep curl \
    && rm -rf /var/lib/apt/lists/*

# TODO: pin a version if messy
RUN npm install -g @earendil-works/pi-coding-agent

COPY --chown=node:node rec-ext /opt/rec

RUN mkdir -p /home/node/.pi \
    && chown node:node /home/node/.pi

USER node
WORKDIR /work
