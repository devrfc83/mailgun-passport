![Mailgun Passport logo](https://raw.githubusercontent.com/devrfc83/mailgun-passport/main/logo.jpg)

# Mailgun Passport

A small SMTP relay that accepts mail from your applications and delivers it through the [Mailgun](https://www.mailgun.com/) HTTP API. Use it when an application only speaks SMTP but the host cannot send outbound SMTP, which many cloud providers block.

Mailgun Passport is an independent project. It is not affiliated with or endorsed by Mailgun.

- Source, issues and full documentation: [github.com/devrfc83/mailgun-passport](https://github.com/devrfc83/mailgun-passport)
- Also published as `ghcr.io/devrfc83/mailgun-passport`
- License: [MIT](https://github.com/devrfc83/mailgun-passport/blob/main/LICENSE)

## Tags and platforms

| Tag | Points to |
| --- | --- |
| `X.Y.Z` | one release, never moved |
| `X.Y` | the latest patch release of `X.Y` |
| `latest` | the latest release |

Every tag is a multi-arch image for `linux/amd64` and `linux/arm64`, with an SBOM and provenance attestation. Pin `X.Y` in production.

The image is distroless (no shell, no package manager), about 14 MB, and runs as UID 65532. It listens for SMTP on port **2525**, since a non-root process cannot bind 587, and serves `/health` and `/ready` on port **8080**.

## Quick start

```bash
docker run -d --name mailgun-passport --restart unless-stopped \
  -p 127.0.0.1:587:2525 \
  -e MAILGUN_API_KEY=key-... \
  -e MAILGUN_DOMAIN=mg.example.com \
  -e SMTP_USERNAME=relay -e SMTP_PASSWORD=change-me \
  -e SMTP_ALLOW_INSECURE_AUTH=true \
  --read-only --cap-drop=ALL --security-opt no-new-privileges \
  seadragoncl/mailgun-passport:latest

docker logs -f mailgun-passport
```

Applications on the host now send to `127.0.0.1:587` with user `relay`. For a domain in Mailgun's EU region, add `-e MAILGUN_API_BASE=eu`.

At startup the relay checks the API key and domain with a test-mode call that sends nothing. If Mailgun rejects them, the container exits with status 2 and the log says why.

Inside the container the relay listens on `0.0.0.0`, which is not loopback, so it needs one of these:

- `SMTP_USERNAME`/`SMTP_PASSWORD`, as above. Without STARTTLS, `SMTP_ALLOW_INSECURE_AUTH=true` is also required, because the password then crosses the network in clear text. That is acceptable when the port is published only on `127.0.0.1`.
- `SMTP_ALLOW_UNAUTHENTICATED=true`, when only trusted containers on a private network can reach it (see [Compose](#docker-compose)).

Otherwise it refuses to start rather than run as an open relay.

## Docker Compose

API key and SMTP password as [Compose secrets](https://docs.docker.com/compose/how-tos/use-secrets/), read through the `_FILE` variables:

```yaml
services:
  mailgun-passport:
    image: seadragoncl/mailgun-passport:latest
    restart: unless-stopped
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    ports:
      - "127.0.0.1:587:2525"
    environment:
      MAILGUN_DOMAIN: mg.example.com
      MAILGUN_API_KEY_FILE: /run/secrets/mailgun_api_key
      SMTP_USERNAME: relay
      SMTP_PASSWORD_FILE: /run/secrets/smtp_password
      SMTP_ALLOW_INSECURE_AUTH: "true"
    secrets: [mailgun_api_key, smtp_password]

secrets:
  mailgun_api_key:
    file: ./secrets/mailgun-api-key
  smtp_password:
    file: ./secrets/smtp-password
```

Outside Swarm, Compose bind-mounts secret files with their host owner and mode, so UID 65532 must be able to read them. Keep them in a directory only you can enter and make the files themselves world-readable, or `chown 65532` them. Only the trailing line break is removed from the file.

If the relay only serves other containers in the same project, drop `ports:` and authentication. Those containers then send to `mailgun-passport:2525`:

```yaml
    environment:
      MAILGUN_DOMAIN: mg.example.com
      MAILGUN_API_KEY_FILE: /run/secrets/mailgun_api_key
      SMTP_ALLOW_UNAUTHENTICATED: "true"
```

## Things Docker changes

- **Published ports bypass the host firewall.** Docker writes its own iptables/nftables rules, so firewalld and ufw do not filter `-p 587:2525`. Publish on `127.0.0.1` unless other hosts must connect, and then require AUTH with STARTTLS.
- **Client addresses may be hidden.** Connections to a port published on `127.0.0.1` go through `docker-proxy`, so inside the container they all come from the bridge gateway (such as `172.17.0.1`), not from loopback. Rootless Podman shows them as the host's own LAN address instead, with the same effect. The relay blocks `AUTH` for 15 minutes after `SMTP_AUTH_MAX_FAILURES` (default 10) failures from one address. Behind `docker-proxy`, one application with a wrong password can therefore lock out every other application on the host. If only the host's own applications connect, set `SMTP_AUTH_MAX_FAILURES=0`.
- **`--env-file` keeps quotes.** Write plain `KEY=value` lines, with no quotes, no `$` and no comments at the end of a line. A value wrapped in quotes stops the relay at startup instead of being sent to Mailgun as part of the key.
- **The `.env` file is not read on its own.** The relay only reads variables from the environment, or from the file named by `PASSPORT_ENV_FILE`.

## Health checks

The image sets `HEALTH_LISTEN=0.0.0.0:8080` and a `HEALTHCHECK` that runs `mailgun-passport -healthcheck`, since the image has no curl. `docker ps` shows the container as `healthy` or `unhealthy`.

| Endpoint | `200` when |
| --- | --- |
| `GET /health` | the process runs and the SMTP listener accepts connections |
| `GET /ready` | the same, and the last Mailgun call did not fail in a way that makes clients retry |

A `503` from `/ready` means clients are getting `451` right now, for example during a Mailgun outage. Use it for alerts and load balancers, and `/health` to decide on restarts. Port 8080 does not need to be published for the `HEALTHCHECK`; publish it, on `127.0.0.1`, only if something outside the container polls it. To turn the endpoints off, set `HEALTH_LISTEN=` (empty) and also disable the `HEALTHCHECK` (`--no-healthcheck`, or `healthcheck: {disable: true}` in Compose), which would otherwise mark the container `unhealthy`.

## Configuration

The most common settings:

| Variable | Default in the image | Description |
| --- | --- | --- |
| `MAILGUN_API_KEY` / `MAILGUN_API_KEY_FILE` | — | **Required.** The key, or a file that holds it |
| `MAILGUN_DOMAIN` | — | **Required.** Sending domain |
| `MAILGUN_API_BASE` | `us` | `us`, `eu`, or a full URL |
| `SMTP_USERNAME` / `SMTP_PASSWORD` / `SMTP_PASSWORD_FILE` | empty | Require `AUTH PLAIN` or `AUTH LOGIN` |
| `SMTP_ALLOW_INSECURE_AUTH` | `false` | Allow AUTH without STARTTLS |
| `SMTP_ALLOW_UNAUTHENTICATED` | `false` | Accept mail without AUTH |
| `SMTP_TLS_CERT_FILE` / `SMTP_TLS_KEY_FILE` | empty | Enable STARTTLS; reloaded when either file changes |
| `SMTP_ALLOWED_SENDER_DOMAINS` | organization of `MAILGUN_DOMAIN` | Allowed sender domains; `*` allows any |
| `SMTP_AUTH_MAX_FAILURES` | `10` | Failed AUTH per client address before a 15-minute block; `0` disables |
| `SMTP_LISTEN` | `0.0.0.0:2525` | Listen address inside the container |
| `HEALTH_LISTEN` | `0.0.0.0:8080` | Address for `/health` and `/ready`; empty disables them |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

By default only senders in the Mailgun domain's organization are accepted: with `MAILGUN_DOMAIN=mg.example.com`, that is `example.com` and any `*.example.com`. Every setting is described in the [main README](https://github.com/devrfc83/mailgun-passport#configuration).

## Send a test message

With [swaks](https://jetmore.org/john/code/swaks/) on the host:

```bash
swaks --server 127.0.0.1:587 --auth PLAIN --auth-user relay --auth-password change-me \
  --from you@example.com --to dest@example.com \
  --header "Subject: Passport" --body "Hello"
```

Mail is not queued: the client gets `250` only after Mailgun accepts the message, and `451` when it should retry later. Applications that do not retry lose mail during a Mailgun outage. The [main README](https://github.com/devrfc83/mailgun-passport#delivery-retries-and-duplicates) shows how to put Postfix in front of the relay for a real queue.
