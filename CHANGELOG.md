# Changelog

Each `## vX.Y.Z` section becomes the notes of the GitHub release for that tag. The release workflow fails if the section is missing.

## v0.3.1

### Fixed

- `parsed` and `mime` modes now decode header fields the same way. `mime` mode used a decoder meant for MIME parameters: it replaced invalid UTF-8 with `U+FFFD`, so the two modes could split recipients into To, Cc and Bcc differently, and after an encoded word it put quotes around any text containing `=`, which could change a `Subject`. Both modes now use one decoder, which leaves a value it cannot decode as it is. Found by the fuzz tests.

## v0.3.0

### Added

- **On-disk queue.** With `QUEUE_DIR` set, each message is stored on disk, flushed before the client gets `250`, and delivered to Mailgun in the background with retries (30 seconds, doubling up to 15 minutes). A Mailgun outage no longer reaches applications that do not retry on `4xx`, and queued mail survives a restart. `QUEUE_MAX_BYTES` (default 256 MiB), `QUEUE_MAX_AGE` (default 24 hours) and `QUEUE_WORKERS` (default 2) tune it. A message that Mailgun refuses for good, or that expires, moves to `QUEUE_DIR/failed/` and is logged and counted; **nobody is told by email**, and the relay never deletes those files. When the queue is full or its disk fails, clients get `451`. Off by default. See "The on-disk queue" in the README.
- **Metrics.** `METRICS_ENABLED=true` serves `GET /metrics` in the Prometheus text format on `HEALTH_LISTEN`: messages by outcome, Mailgun calls by status with a duration histogram, SMTP connections, AUTH attempts, rate-limited messages, reloads, queue deliveries and size, and build information. Off by default. No new dependency.
- **`SIGHUP` reloads the configuration** without dropping connections: `*_FILE` secrets and the `PASSPORT_ENV_FILE` file are read again, the STARTTLS certificate is reloaded, `LOG_FILE` is reopened (logrotate without `copytruncate`), and the settings that can change live are applied: the Mailgun key, domain, region, timeout, test mode and options, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_ALLOWED_SENDER_DOMAINS` and `LOG_LEVEL`. A new key or domain is checked with Mailgun first, and an invalid configuration is refused with the current one left running. Settings that need a restart are named in the log. Turning `AUTH` on or off needs a restart. `systemctl reload` and the Quadlet unit send it. See "Reloading the configuration".
- `SMTP_RATE_LIMIT_PER_MINUTE` (off by default): messages per minute per client address, with a burst of the same size. Over it, `MAIL FROM` gets `451 4.7.1`.
- Mailgun options: `MAILGUN_TAGS`, `MAILGUN_TRACKING_CLICKS`, `MAILGUN_TRACKING_OPENS` and `MAILGUN_REQUIRE_TLS` set defaults for every message, and the `X-Mailgun-Tag`, `-Track`, `-Track-Clicks`, `-Track-Opens`, `-Dkim`, `-Require-Tls`, `-Skip-Verification` and `-Variables` header fields set them per message, in both send modes. Invalid values are ignored and logged. `MAILGUN_TESTMODE` cannot be set by a header.
- Releases are signed with Sigstore cosign, keyless: `SHA256SUMS.sigstore.json` for the packages' checksums, and the container images by digest. The README shows how to verify them.
- [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md).
- Kubernetes manifests in `contrib/kubernetes/`: Deployment, Service and NetworkPolicy.

### Container image

- The image has `/var/lib/mailgun-passport`, owned by its user and declared as a `VOLUME`, for the queue: a named volume mounted there is writable even with `--read-only`.

### Changed

- The systemd unit has `ExecReload=` (`SIGHUP`) and `StateDirectory=mailgun-passport`, so the service can write to `/var/lib/mailgun-passport` under `ProtectSystem=strict`. systemd creates that directory when the service starts. The Quadlet unit names its container `mailgun-passport`, for `ExecReload=`.
- `SIGHUP` used to end the process, as it does for any Go program that does not handle it. It now reloads. A terminal that closes sends `SIGHUP` to its foreground processes: run the relay as a service.
- The `X-Mailgun-*` header fields listed above are now turned into API parameters. Applications that already set them see them take effect; before, `parsed` mode ignored them.

### Development

- Fuzz tests for the parser (`make fuzz`; CI runs 20 seconds per target on every change, and `fuzz.yml` ten minutes per target every Monday). Tests for `main`, the connection limit and the new code, with an end-to-end test that starts the relay against a fake Mailgun. Total coverage went from 81% to 92%, and `make cover` fails below 90%.
- `make lint` also runs golangci-lint, configured in `.golangci.yml`, at a pinned version.

## v0.2.0

### Breaking changes

- **`.env` is no longer read implicitly.** The relay used to load `.env` from the working directory and from the binary's directory. Now it reads only the environment, plus the file named by `PASSPORT_ENV_FILE`, which must exist. The packages and the container image are not affected: systemd's `EnvironmentFile=` and `--env-file` still work. If you run the binary next to a `.env`, start it with `PASSPORT_ENV_FILE=.env`.
- **Sender domains are restricted by default.** With `SMTP_ALLOWED_SENDER_DOMAINS` empty, only the organization of `MAILGUN_DOMAIN` and its subdomains are accepted, in `MAIL FROM` and in the `From` header. For `mg.example.com`, those are `example.com` and `*.example.com`. Other senders now get `550 5.7.1`. List your domains in `SMTP_ALLOWED_SENDER_DOMAINS`, or set it to `*` to allow any sender as before.
- **A rejected API key, domain or region stops the relay at startup.** It checks them with an empty test-mode call, which sends nothing. On `401`, `403` or `404` it exits with status 2, which the systemd unit does not restart. Before, it started and answered `451` to every message. If Mailgun cannot be reached, it still starts and logs a warning.

### Added

- `AUTH LOGIN`, alongside `AUTH PLAIN`, for clients that offer nothing else.
- `SMTP_AUTH_MAX_FAILURES` (default 10): after that many failed `AUTH` attempts from one address, on any number of connections, `AUTH` is refused for 15 minutes. IPv6 clients are counted per `/64`; loopback clients are exempt. Behind Docker or rootless Podman port publishing, clients do not appear as loopback and share one count: see the [security model](https://github.com/devrfc83/mailgun-passport#security-model).
- `SMTP_MAX_CONNECTIONS` (default 100). Connections over the limit get `421 4.7.0` instead of the greeting.
- `HEALTH_LISTEN` serves `GET /health` and `GET /ready`. It is off by default, except in the container image (see below). `mailgun-passport -healthcheck` queries `/health` for images without curl.
- `MAILGUN_API_KEY_FILE` and `SMTP_PASSWORD_FILE` read secrets from files, such as container secrets or systemd `LoadCredential=`.
- The STARTTLS certificate is reloaded when `SMTP_TLS_CERT_FILE` or `SMTP_TLS_KEY_FILE` changes, without a restart. An invalid new pair, such as a half-written renewal, is ignored and the previous one is kept.
- `go install github.com/devrfc83/mailgun-passport/cmd/mailgun-passport@latest` works: the Go module was renamed to its repository path.
- `SMTP_ALLOWED_SENDER_DOMAINS` accepts `*.example.com` for subdomains, and `*` alone for any sender.

### Container image

- The image sets `HEALTH_LISTEN=0.0.0.0:8080`, exposes port 8080, and has a `HEALTHCHECK`. To turn the endpoints off, set `HEALTH_LISTEN=` and disable the health check (`--no-healthcheck`).
- The Quadlet unit in `contrib/podman/` restarts the container when `/health` keeps failing.
- Base images are pinned by digest.
- The new [README.docker.md](https://github.com/devrfc83/mailgun-passport/blob/main/README.docker.md) covers Docker and Docker Compose.

### Changed

- In `mime` mode, the default, only the message headers are parsed. Large attachments are no longer decoded and kept in memory twice.
- Repeated `RCPT TO` addresses are merged, so `mime` mode no longer delivers two copies.
- Values wrapped in quotes stop the relay at startup, because Docker and Podman `--env-file` would pass the quotes on as part of the value. The value itself is not logged.
- Subjects are logged only at `debug` level, since they can carry personal data.
- Building from source, including `go install`, needs Go 1.27 or newer. Packages and images are not affected.
- Startup logs list the allowed sender domains and the subject and expiry of the TLS certificate, and warn when `MAILGUN_API_BASE` uses plain `http://`.

### Fixed

- `golang.org/x/net` 0.62.0 and `golang.org/x/text` 0.43.0 fix GO-2026-6860, a quadratic-time case in the HTML parser that `parsed` mode reaches through enmime, and other advisories in code the relay does not call.
- Messages over `SMTP_MAX_MESSAGE_BYTES` get `552 5.3.4` instead of `550 5.6.0`.
- In `parsed` mode, attachments and inline parts keep their `Content-Type`. Before, Mailgun had to guess it from the file name, which failed for inline images named after their Content-ID.
- Mailgun error text passed on in SMTP replies and logs is no longer cut in the middle of a UTF-8 character.

## v0.1.1

- No changes to the relay. Adds the release workflow, which tests the packages in 15 distributions and publishes the multi-arch image to GHCR and Docker Hub.

## v0.1.0

- First release: `.rpm` and `.deb` packages for amd64 and arm64, and a container image.
