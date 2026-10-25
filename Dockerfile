# Build stage: runs on the build host's platform and cross-compiles, so
# `--platform linux/arm64` does not need emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27 AS builder

ARG VERSION=0.0.0
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/mailgun-passport ./cmd/mailgun-passport

# Runtime stage: static binary, CA certificates, non-root user (65532), no shell.
FROM gcr.io/distroless/static-debian13:nonroot

ARG VERSION=0.0.0
LABEL org.opencontainers.image.title="mailgun-passport" \
      org.opencontainers.image.description="Local SMTP relay that forwards mail to the Mailgun HTTP API" \
      org.opencontainers.image.source="https://github.com/devrfc83/mailgun-passport" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=builder /out/mailgun-passport /usr/bin/mailgun-passport

# Unprivileged port: binding 587 as non-root fails under Podman and Kubernetes.
# Publish it as 587 on the host if needed (-p 127.0.0.1:587:2525).
ENV SMTP_LISTEN=0.0.0.0:2525
EXPOSE 2525

USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/mailgun-passport"]
