# Mailgun Passport

A lightweight SMTP relay that accepts mail on localhost and forwards it to the [Mailgun](https://www.mailgun.com/) HTTP API. It parses MIME messages, supports both Mailgun's message API and raw MIME delivery, and is designed to run as a systemd service with minimal configuration.

Environment variables are read from the process (including `Environment=` / `EnvironmentFile=` from systemd). If any are missing, values are filled from `.env` **without overriding** variables already defined by systemd.

## Overview

Mailgun Passport acts as a local SMTP submission endpoint for applications that only support SMTP but need to send transactional email through Mailgun. Many cloud providers restrict outbound SMTP traffic, making a local relay that translates SMTP to Mailgun's API a practical solution.

## Requirements

- Go 1.25+
- A domain and API key from [Mailgun](https://app.mailgun.com/settings/api_security)

## Configuration

Copy `.env.example` to `.env` or define the same keys in the systemd unit.

| Variable | Default | Description |
| --- | --- | --- |
| `SMTP_LISTEN` | `127.0.0.1:587` | SMTP listen address |
| `SMTP_DOMAIN` | `localhost` | EHLO domain name |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | empty | If both are set, requires `AUTH PLAIN` |
| `SMTP_ALLOW_INSECURE_AUTH` | `true` | Allow AUTH without TLS (localhost) |
| `SMTP_TLS_CERT_FILE` / `SMTP_TLS_KEY_FILE` | empty | Optional STARTTLS |
| `MAILGUN_API_KEY` | — | Required |
| `MAILGUN_DOMAIN` | — | Required |
| `MAILGUN_API_BASE` | `us` | `us`, `eu`, or full URL |
| `MAILGUN_SEND_MODE` | `parsed` | `parsed` (API fields) or `mime` (raw MIME) |
| `MAILGUN_TESTMODE` | `false` | Mailgun `o:testmode` flag |
| `PASSPORT_ENV_FILE` | — | Alternative path to `.env` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FILE` | `/var/log/mailgun-passport.log` | Log file path (set to `-` to log only to stdout) |

In `parsed` mode, From, To, Cc, Bcc, Subject, text, HTML, and attachments are extracted and sent via `POST /v3/{domain}/messages`. Recipients only present in the SMTP envelope (`RCPT TO`) and not in headers are sent as Bcc.

## Usage

```bash
cp .env.example .env
# edit MAILGUN_API_KEY and MAILGUN_DOMAIN
go run ./cmd/mailgun-passport
```

Local test (no AUTH):

```bash
swaks --to dest@example.com --from you@YOUR_DOMAIN \
  --server 127.0.0.1:587 --port 587 \
  --header "Subject: Passport" --body "Hello"
```

## systemd

```bash
sudo install -m 0755 mailgun-passport /usr/local/bin/mailgun-passport
sudo install -m 0644 contrib/systemd/mailgun-passport.service /etc/systemd/system/
sudo install -m 0600 .env.example /etc/mailgun-passport.env
# edit /etc/mailgun-passport.env
sudo useradd --system --home /opt/mailgun-passport --shell /usr/sbin/nologin mailgun-passport
sudo systemctl daemon-reload
sudo systemctl enable --now mailgun-passport
```

The unit includes `EnvironmentFile=-/etc/mailgun-passport.env` and commented examples of `Environment=` (equivalent to `SetEnv` in some templates).

## Building packages

### Requirements

**For local RPM builds (Fedora/RHEL/Alma/Rocky):**
- `rpm-build`
- `golang >= 1.25`
- `systemd-rpm-macros` (usually pulled in by `rpm-build`)

**For local DEB builds (Debian/Ubuntu):**
- `build-essential`
- `debhelper` / `debhelper-compat`
- `dh-systemd` or `debhelper` with systemd support
- `golang-go >= 1.25` or `golang-1.25-go`
- `devscripts`

### Build with Make

```bash
# Build RPMs for Fedora 43/44, Rocky/Alma/RHEL 8/9/10
make rpm

# Build DEBs for Ubuntu 24.04-26.10 and Debian 13
make deb

# Build a specific distro
make rpm-fedora43
make deb-ubuntu-24.04
make deb-debian13
```

Packages are placed under `build/pkg/rpm/...` and `build/pkg/deb/...`. You can override the version with `VERSION=x.y.z make rpm`.

### Build using Docker/Podman (no local build tools required)

If you don't have `rpm-build` or `dpkg-buildpackage` installed, you can build packages inside a container:

```bash
# RPM (Docker)
make docker-rpm

# DEB (Docker)
make docker-deb

# RPM (Podman)
make podman-rpm

# DEB (Podman)
make podman-deb
```

### Build and push a Docker image

Build a container image for the service:

```bash
# Build the image locally
make docker-image

# Or explicitly
docker build -t mailgun-passport:latest .
```

Tag and push to a registry:

```bash
# Docker Hub
docker tag mailgun-passport:latest youruser/mailgun-passport:latest
docker tag mailgun-passport:latest youruser/mailgun-passport:0.1.0
docker login
docker push youruser/mailgun-passport:latest
docker push youruser/mailgun-passport:0.1.0

# GitHub Container Registry (ghcr.io)
docker tag mailgun-passport:latest ghcr.io/yourorg/mailgun-passport:latest
docker tag mailgun-passport:latest ghcr.io/yourorg/mailgun-passport:0.1.0
echo $GITHUB_TOKEN | docker login ghcr.io -u youruser --password-stdin
docker push ghcr.io/yourorg/mailgun-passport:latest
docker push ghcr.io/yourorg/mailgun-passport:0.1.0
```

## Docker usage

```bash
# Run with environment variables
docker run -d \
  --name mailgun-passport \
  -p 587:587 \
  -e MAILGUN_API_KEY=your-key \
  -e MAILGUN_DOMAIN=your-domain.com \
  -e SMTP_USERNAME=relay \
  -e SMTP_PASSWORD=relaypass \
  -v $(pwd)/.env:/etc/mailgun-passport.env:ro \
  mailgun-passport:latest

# Or use an env file
docker run -d \
  --name mailgun-passport \
  -p 587:587 \
  --env-file .env \
  mailgun-passport:latest
```
