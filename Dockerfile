FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o /qvole ./cmd/qvole

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
RUN adduser -D -H -u 1000 qvole
COPY --from=builder /qvole /usr/local/bin/qvole
USER qvole
EXPOSE 9009/udp
ENTRYPOINT ["qvole"]
