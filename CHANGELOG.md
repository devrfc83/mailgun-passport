# Changelog

Each `## vX.Y.Z` section becomes the notes of the GitHub release for that tag. The release workflow fails if the section is missing.

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
- Building from source needs Go 1.26 or newer, required by the fixed `golang.org/x/net`.
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
