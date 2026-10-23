ARG ALPINE_TAG=3.22
FROM alpine:${ALPINE_TAG}
RUN apk add --no-cache bash openssl curl jq && adduser -D -u 1000 minter
COPY --chmod=755 github-token.sh refresh.sh /usr/local/bin/
USER minter
ENTRYPOINT ["/usr/local/bin/refresh.sh"]
