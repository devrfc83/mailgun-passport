# Build stage
FROM golang:1.25-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.Version=${VERSION:-dev} -X main.Commit=${COMMIT:-unknown}" -o mailgun-passport ./cmd/mailgun-passport

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --home /opt/mailgun-passport --shell /usr/sbin/nologin mailgun-passport && \
    mkdir -p /opt/mailgun-passport /var/log && \
    touch /var/log/mailgun-passport.log && \
    chown mailgun-passport:mailgun-passport /var/log/mailgun-passport.log

COPY --from=builder /app/mailgun-passport /usr/bin/mailgun-passport
COPY --from=builder /app/.env.example /etc/mailgun-passport.env.example

ENV SMTP_LISTEN=0.0.0.0:587
ENV SMTP_DOMAIN=localhost
ENV LOG_FILE=/var/log/mailgun-passport.log
ENV LOG_LEVEL=info

USER mailgun-passport

EXPOSE 587

ENTRYPOINT ["/usr/bin/mailgun-passport"]
