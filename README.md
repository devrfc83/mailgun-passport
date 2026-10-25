# Mailgun Passport

A small SMTP relay that accepts mail from local applications and delivers it through the [Mailgun](https://www.mailgun.com/) HTTP API. Use it when an application only speaks SMTP but the host cannot send outbound SMTP, which many cloud providers block.

Mailgun Passport is an independent project. It is not affiliated with or endorsed by Mailgun.

## How it works

- Clients submit mail over SMTP (port 587 by default, optional `AUTH PLAIN` and STARTTLS).
- **The SMTP envelope decides who receives the message.** Only the `RCPT TO` addresses get a copy. Addresses that appear only in the `To`/`Cc` headers do not, which is what an MTA that sends one copy per recipient expects.
- In `mime` mode (the default), the original message goes unchanged to `POST /v3/{domain}/messages.mime`, except that the `Bcc` header is removed. Every header, the MIME structure and inline images stay intact.
- In `parsed` mode, the message is rebuilt from its fields (From, To, Cc, Subject, text, HTML, attachments) and sent to `POST /v3/{domain}/messages`.
- Delivery is synchronous: the client gets its SMTP reply only after Mailgun answers. Nothing is queued locally, so clients must retry on `4xx`:

| Mailgun result | SMTP reply | Meaning for the client |
| --- | --- | --- |
| accepted | `250` | done |
| unreachable, timeout, `429`, `5xx` | `451 4.4.0` | retry later |
| `401`, `403`, `404` (bad key or domain) | `451 4.4.0` | retry later; the operator must fix the configuration |
| other `4xx` (e.g. invalid address) | `554 5.0.0` | permanent failure, do not retry |

## Security model

- With the default `SMTP_LISTEN=127.0.0.1:587`, only local processes can connect.
- On any other address, the relay **refuses to start** without `SMTP_USERNAME`/`SMTP_PASSWORD`, so it cannot become an open relay by accident. Set `SMTP_ALLOW_UNAUTHENTICATED=true` only when a firewall or private network already restricts who can connect.
- Plain-text `AUTH` is allowed by default only on loopback. Elsewhere, configure STARTTLS or set `SMTP_ALLOW_INSECURE_AUTH=true` explicitly.
- After 3 failed `AUTH` attempts, the connection must reconnect. Every failure costs one second.
- `SMTP_ALLOWED_SENDER_DOMAINS` limits which domains may appear in `MAIL FROM` and the `From` header.

## Install from packages

`make packages` builds one `.rpm` and one `.deb` per architecture (`amd64`, `arm64`). The binary is static, so the same packages work on:

- Fedora 43 and 44
- RHEL, AlmaLinux and Rocky Linux 8, 9 and 10
- Debian 12 and 13
- Ubuntu 24.04, 26.04 and 26.10

`make test-packages` checks all of these in containers.

```bash
sudo dnf install ./dist/mailgun-passport-*.x86_64.rpm     # Fedora / EL
sudo apt install ./dist/mailgun-passport_*_amd64.deb      # Debian / Ubuntu

sudoedit /etc/mailgun-passport/mailgun-passport.env      # set MAILGUN_API_KEY and MAILGUN_DOMAIN
sudo systemctl enable --now mailgun-passport
journalctl -u mailgun-passport -f
```

The package installs:

| Path | Purpose |
| --- | --- |
| `/usr/bin/mailgun-passport` | the relay |
| `/etc/mailgun-passport/mailgun-passport.env` | configuration, `0600 root`, preserved on upgrade |
| `/usr/lib/systemd/system/mailgun-passport.service` | hardened unit (`systemd-analyze security`: 1.4–1.6, "OK") |
| `/usr/lib/sysusers.d/mailgun-passport.conf` | the `mailgun-passport` system user |

The service is not enabled automatically, because it cannot run until the API key is set. Upgrades restart it only if it was already running. An invalid configuration exits with status 2, which the unit does not restart.

To change single settings without editing the env file, use `sudo systemctl edit mailgun-passport` and add `Environment=` lines.

## Configuration

Settings come from the process environment (for example systemd's `EnvironmentFile=`). Missing ones are filled from `.env` in the working directory, or from the file named by `PASSPORT_ENV_FILE`, without overriding variables that are already set. Invalid values stop the relay with an error. They never fall back to defaults silently. [.env.example](.env.example) documents every setting.

| Variable | Default | Description |
| --- | --- | --- |
| `MAILGUN_API_KEY` | — | **Required** |
| `MAILGUN_DOMAIN` | — | **Required** sending domain |
| `MAILGUN_API_BASE` | `us` | `us`, `eu`, or a full URL |
| `MAILGUN_SEND_MODE` | `mime` | `mime` (forward the original) or `parsed` (rebuild from fields) |
| `MAILGUN_TIMEOUT` | `30s` | Timeout per API call |
| `MAILGUN_TESTMODE` | `false` | Mailgun `o:testmode` |
| `SMTP_LISTEN` | `127.0.0.1:587` | Listen address |
| `SMTP_DOMAIN` | `localhost` | Name announced in the SMTP greeting |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | empty | Setting both requires `AUTH PLAIN` |
| `SMTP_ALLOW_INSECURE_AUTH` | `true` on loopback, else `false` | Allow AUTH without STARTTLS |
| `SMTP_TLS_CERT_FILE` / `SMTP_TLS_KEY_FILE` | empty | Enable STARTTLS |
| `SMTP_ALLOW_UNAUTHENTICATED` | `false` | Allow a non-loopback listener without AUTH |
| `SMTP_ALLOWED_SENDER_DOMAINS` | empty (any) | Comma-separated allowed sender domains |
| `SMTP_MAX_MESSAGE_BYTES` | `10485760` | Maximum message size |
| `SMTP_MAX_RECIPIENTS` | `50` | Maximum recipients per message |
| `SMTP_READ_TIMEOUT` / `SMTP_WRITE_TIMEOUT` | `30s` | Per-command timeouts |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FILE` | empty | Also log to this file. Logs always go to stderr. |
| `PASSPORT_ENV_FILE` | — | Explicit env file. It must exist. |

## Run from source

Requires Go 1.25 or newer.

```bash
cp .env.example .env        # set MAILGUN_API_KEY and MAILGUN_DOMAIN
go run ./cmd/mailgun-passport

swaks --server 127.0.0.1:587 --to dest@example.com --from you@YOUR_DOMAIN \
  --header "Subject: Passport" --body "Hello"
```

## Containers

The image is distroless, about 14 MB, and runs as a non-root user. It listens on port **2525**, because non-root processes cannot bind 587 under Podman or Kubernetes. It logs to stderr.

```bash
make image                  # podman, or docker with CONTAINER=docker

podman run -d --name mailgun-passport \
  -p 127.0.0.1:587:2525 \
  -e MAILGUN_API_KEY=key-... -e MAILGUN_DOMAIN=mg.example.com \
  -e SMTP_USERNAME=relay -e SMTP_PASSWORD=change-me \
  -e SMTP_ALLOW_INSECURE_AUTH=true \
  --read-only --cap-drop=ALL \
  mailgun-passport:latest
```

`--env-file .env` works as well. Publish the port on `127.0.0.1`, as above, unless other hosts must reach it. Docker's published ports bypass firewalld and ufw. If other containers on a private network reach the relay without AUTH, set `SMTP_ALLOW_UNAUTHENTICATED=true`.

For Podman under systemd, [contrib/podman/mailgun-passport.container](contrib/podman/mailgun-passport.container) is a Quadlet unit. Copy it to `/etc/containers/systemd/`, then run `systemctl daemon-reload && systemctl start mailgun-passport`.

Released images are published to `ghcr.io/devrfc83/mailgun-passport` and Docker Hub for `linux/amd64` and `linux/arm64`, tagged `X.Y.Z`, `X.Y` and `latest` (see [Releasing](#releasing)).

## Building

| Target | Result |
| --- | --- |
| `make build` | `dist/mailgun-passport` for this machine |
| `make test` | `go vet` and the test suite with the race detector |
| `make packages` | `.rpm` and `.deb` for `ARCHES` (default `amd64 arm64`) in `dist/` |
| `make rpm` / `make deb` | a single package format |
| `make container-packages` | the same packages built inside `golang:1.27`; only Podman or Docker is needed |
| `make test-packages` | installs, checks and removes the packages in 15 distribution containers |
| `make image` | the container image |

Packages are built with [nFPM](https://nfpm.goreleaser.com), which `go run` fetches at a pinned version. No `rpmbuild`, `dpkg-buildpackage` or distribution Go toolchain is involved.

Versions come from `vX.Y.Z` git tags (see [scripts/version.sh](scripts/version.sh)). A tagged commit builds `X.Y.Z`. Later commits build `X.Y.Z+N.gHASH`, plus `.dirty` with uncommitted changes, which sorts after the release in both rpm and dpkg. Override with `make packages VERSION=1.2.3`. `mailgun-passport -version` prints the embedded version.

## Releasing

Pushing a `vX.Y.Z` tag runs [.github/workflows/release.yml](.github/workflows/release.yml):

1. `make test`
2. `make packages` and `make test-packages`, after checking that `scripts/version.sh` matches the tag
3. a multi-arch image (amd64 and arm64), with SBOM and provenance, pushed to GHCR and Docker Hub
4. a GitHub release with the `.rpm` and `.deb` files and `SHA256SUMS`. It is created only after steps 2 and 3 succeed. Tags with a suffix such as `v1.0.0-rc1` become prereleases and do not move `latest`.

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

One-time setup in GitHub, under Settings → Secrets and variables → Actions:

| Kind | Name | Value |
| --- | --- | --- |
| Variable | `DOCKERHUB_NAMESPACE` | the Docker Hub organization |
| Secret | `DOCKERHUB_TOKEN` | an organization access token with read/write access to `<namespace>/mailgun-passport` |
| Variable | `DOCKERHUB_USERNAME` | optional. Only needed with a personal access token: the Docker Hub user that owns it. Defaults to `DOCKERHUB_NAMESPACE`. |

The GHCR package is created private on the first push. Make it public under the package settings on GitHub.
