ARG GO_VERSION_TAG=1.25-alpine

# build
FROM golang:${GO_VERSION_TAG} AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /extproc .

# application
FROM golang:${GO_VERSION_TAG}

COPY --from=builder /extproc /extproc

EXPOSE 50051
EXPOSE 9090

CMD ["/extproc"]
