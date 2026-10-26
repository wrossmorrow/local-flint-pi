ARG GO_VERSION_TAG=1.25

# build
FROM golang:${GO_VERSION_TAG} AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /extproc .
RUN go install github.com/grpc-ecosystem/grpc-health-probe@v0.4.39

# application
FROM golang:${GO_VERSION_TAG}

COPY --from=builder /extproc /extproc
COPY --from=builder /go/bin/grpc-health-probe /grpc-health-probe

EXPOSE 50051
EXPOSE 9090
EXPOSE 9091

CMD ["/extproc"]
