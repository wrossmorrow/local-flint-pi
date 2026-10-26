ARG GO_VERSION_TAG=1.25

# build
FROM golang:${GO_VERSION_TAG} AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /extproc .
RUN go install github.com/grpc-ecosystem/grpc-health-probe@v0.4.39

# pre-install the motherduck extension using the duckdb runtime bundled with the driver
RUN go build -o /tmp/md-install ./mdinstall && /tmp/md-install && rm /tmp/md-install

# application
FROM golang:${GO_VERSION_TAG}

COPY --from=builder /extproc /extproc
COPY --from=builder /go/bin/grpc-health-probe /grpc-health-probe
COPY --from=builder /root/.duckdb /root/.duckdb

EXPOSE 50051
EXPOSE 9090
EXPOSE 9091

CMD ["/extproc"]
