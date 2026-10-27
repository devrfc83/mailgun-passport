<p align="center"><img src="logo.jpg" alt="Mailgun Passport logo" width="480"></p>

# Mailgun Passport

A small SMTP relay that accepts mail from local applications and delivers it through the [Mailgun](https://www.mailgun.com/) HTTP API. Use it when an application only speaks SMTP but the host cannot send outbound SMTP, which many cloud providers block.

Mailgun Passport is an independent project. It is not affiliated with or endorsed by Mailgun.

To build it yourself, create packages or images, or cut a release, see [BUILD.md](BUILD.md).

## When to use it

Mailgun also accepts SMTP directly, at `smtp.mailgun.org` (or `smtp.eu.mailgun.org`) on ports 25, 465, 587 and 2525. Many cloud providers block only port 25, so if your host can reach one of the other ports, point the application there: it is simpler and needs nothing installed.

Mailgun Passport is for the cases where that does not work or is not wanted:

- The host blocks every outbound SMTP port, or only lets HTTPS out, possibly through a proxy (`HTTPS_PROXY` is honored).
- Applications should not hold Mailgun credentials. They talk to a local relay; the API key lives in one root-only file or secret.
- Sender domains should be restricted in one place, whatever the applications send.

## How it works

- Clients submit mail over SMTP (port 587 by default, optional `AUTH PLAIN` or `AUTH LOGIN`, and STARTTLS).
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
| message larger than `SMTP_MAX_MESSAGE_BYTES` | `552 5.3.4` | permanent failure, do not retry |
| `SMTP_MAX_CONNECTIONS` sessions already open | `421 4.7.0` instead of the greeting | retry later |
| `SMTP_AUTH_MAX_FAILURES` failed `AUTH` from the client's address | `421 4.7.0` to `AUTH` | retry after 15 minutes |

## Security model

- With the default `SMTP_LISTEN=127.0.0.1:587`, only local processes can connect.
- On any other address, the relay **refuses to start** without `SMTP_USERNAME`/`SMTP_PASSWORD`, so it cannot become an open relay by accident. Set `SMTP_ALLOW_UNAUTHENTICATED=true` only when a firewall or private network already restricts who can connect.
- Plain-text `AUTH` is allowed by default only on loopback. Elsewhere, configure STARTTLS or set `SMTP_ALLOW_INSECURE_AUTH=true` explicitly.
- After 3 failed `AUTH` attempts, the connection must reconnect. Every failure costs one second.
- After `SMTP_AUTH_MAX_FAILURES` (default 10) failed `AUTH` attempts from one address, on any number of connections, that address cannot authenticate until 15 minutes have passed since its last failure. IPv6 clients are counted per `/64`. Loopback clients are exempt, since every local application shares `127.0.0.1` and one with a wrong password would lock out the rest. Behind a proxy, or a container's published port, clients may not appear as loopback (Docker shows the bridge gateway, rootless Podman the host's address), and they all share one count: raise the limit, or set it to `0` to disable it.
- Only senders in the Mailgun domain's organization are accepted by default, in `MAIL FROM` and in the `From` header. With `MAILGUN_DOMAIN=mg.example.com`, that is `example.com` and any `*.example.com`: the senders that Mailgun's DKIM signature aligns with under DMARC. Set `SMTP_ALLOWED_SENDER_DOMAINS` to list other domains, or to `*` to allow any sender.

## Delivery, retries and duplicates

Mailgun Passport keeps nothing on disk. A message is either accepted by Mailgun before the client gets `250`, or the client gets an error and still owns the message. That works well with clients that retry on `4xx`, as every MTA does. Many applications do not: a web app that sends a password reset during a Mailgun outage gets `451` and, usually, drops the message.

If that matters, put a local MTA with a real queue in front of the relay. With Postfix, move the relay to another port with `SMTP_LISTEN=127.0.0.1:2525`, so Postfix can keep 587, and in `/etc/postfix/main.cf` set:

```
relayhost = [127.0.0.1]:2525
```

Applications then send to Postfix (port 25 or the `sendmail` command). Postfix queues the message and retries until the relay answers `250`, and it returns a bounce to the sender on `5xx`.

A message can be delivered twice. When Mailgun accepts it but its answer does not arrive within `MAILGUN_TIMEOUT`, the client gets `451` and sends it again. No SMTP-to-HTTP relay without its own queue can avoid this.

## Install from packages

Each [release](https://github.com/devrfc83/mailgun-passport/releases) has one `.rpm` and one `.deb` per architecture (`x86_64`/`amd64`, `aarch64`/`arm64`), plus `SHA256SUMS`. The binary is static, so the same packages work on:

- Fedora 43 and 44
- RHEL, AlmaLinux and Rocky Linux 8, 9 and 10
- Debian 12 and 13
- Ubuntu 24.04, 26.04 and 26.10

```bash
sudo dnf install ./mailgun-passport-*.x86_64.rpm     # Fedora / EL
sudo apt install ./mailgun-passport_*_amd64.deb      # Debian / Ubuntu

sudoedit /etc/mailgun-passport/mailgun-passport.env  # set MAILGUN_API_KEY and MAILGUN_DOMAIN
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

The service is not enabled automatically, because it cannot run until the API key is set. Upgrades restart it only if it was already running. An invalid configuration exits with status 2, which the unit does not restart. That includes an API key, domain or region that Mailgun rejects: at startup the relay makes an empty test-mode call, which sends nothing. If Mailgun cannot be reached, it starts anyway and logs a warning.

To change single settings without editing the env file, use `sudo systemctl edit mailgun-passport` and add `Environment=` lines.

`mailgun-passport -version` prints the installed version.

## Run the container image

Images for `linux/amd64` and `linux/arm64` are published to `ghcr.io/devrfc83/mailgun-passport` and Docker Hub, tagged `X.Y.Z`, `X.Y` and `latest`. The image is distroless, about 14 MB, and runs as a non-root user. It listens on port **2525**, because non-root processes cannot bind 587 under Podman or Kubernetes. It logs to stderr.

```bash
podman run -d --name mailgun-passport \
  -p 127.0.0.1:587:2525 \
  -e MAILGUN_API_KEY=key-... -e MAILGUN_DOMAIN=mg.example.com \
  -e SMTP_USERNAME=relay -e SMTP_PASSWORD=change-me \
  -e SMTP_ALLOW_INSECURE_AUTH=true \
  --read-only --cap-drop=ALL \
  ghcr.io/devrfc83/mailgun-passport:latest
```

For Docker and Docker Compose, including secrets and how Docker affects the AUTH limit, see [README.docker.md](README.docker.md). `--env-file` works as well (see [Configuration](#configuration) for the file format). Publish the port on `127.0.0.1`, as above, unless other hosts must reach it. Docker's published ports bypass firewalld and ufw. If other containers on a private network reach the relay without AUTH, set `SMTP_ALLOW_UNAUTHENTICATED=true`.

For Podman under systemd, [contrib/podman/mailgun-passport.container](contrib/podman/mailgun-passport.container) is a Quadlet unit. Set its `Image=` to `ghcr.io/devrfc83/mailgun-passport:latest` (it points at a locally built image by default), copy it to `/etc/containers/systemd/`, then run `systemctl daemon-reload && systemctl start mailgun-passport`.

## Health checks

Set `HEALTH_LISTEN` (for example `127.0.0.1:8080`) to serve two plain-text HTTP endpoints. They answer `200 ok`, or `503` with the reason:

| Endpoint | `200` when |
| --- | --- |
| `GET /health` | the process runs and the SMTP listener accepts connections |
| `GET /ready` | the same, and the last Mailgun call did not fail in a way that makes clients retry (unreachable, timeout, `401`/`403`/`404`, `429`, `5xx`) |

`/ready` does not call Mailgun on every request. It reflects the last message sent, or the startup check. While it reports a failure, a request to it re-checks Mailgun in the background at most every 30 seconds, so it recovers even when no mail is flowing. A `503` from `/ready` means clients are getting `451` right now. Use it for alerts and load balancers, and `/health` to decide on restarts: restarting does not fix a Mailgun outage.

`mailgun-passport -healthcheck` queries `/health` and exits `0` when healthy, `1` otherwise. The container image has no shell or curl, so its `HEALTHCHECK` runs this command. The image sets `HEALTH_LISTEN=0.0.0.0:8080`, so Kubernetes probes can reach port 8080 on the pod; the Quadlet unit restarts the container when `/health` keeps failing. Podman keeps a `HEALTHCHECK` only in the Docker image format: `make image` passes `--format docker`.

## Configuration

Settings come from the process environment: systemd's `EnvironmentFile=` for the packages, `-e`/`--env-file` for containers. If `PASSPORT_ENV_FILE` names a file, it fills in the variables that are not already set; that file must exist. No other file is read, not even a `.env` in the working directory. Invalid values stop the relay with an error. They never fall back to defaults silently. [.env.example](.env.example) documents every setting.

The same file may be read by systemd, by Docker or Podman, or by the relay itself, and each one parses it a little differently. Write plain `KEY=value` lines that all of them read the same way: no quotes around values, no `$`, and no comments at the end of a line. A value wrapped in quotes stops the relay at startup, because Docker and Podman would otherwise pass the quotes on as part of the value.

| Variable | Default | Description |
| --- | --- | --- |
| `MAILGUN_API_KEY` | — | **Required**, or `MAILGUN_API_KEY_FILE` |
| `MAILGUN_API_KEY_FILE` | — | Read the API key from this file instead (see [Secrets in files](#secrets-in-files)) |
| `MAILGUN_DOMAIN` | — | **Required** sending domain |
| `MAILGUN_API_BASE` | `us` | `us`, `eu`, or a full URL |
| `MAILGUN_SEND_MODE` | `mime` | `mime` (forward the original) or `parsed` (rebuild from fields) |
| `MAILGUN_TIMEOUT` | `30s` | Timeout per API call |
| `MAILGUN_TESTMODE` | `false` | Mailgun `o:testmode` |
| `SMTP_LISTEN` | `127.0.0.1:587` | Listen address |
| `SMTP_DOMAIN` | `localhost` | Name announced in the SMTP greeting |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | empty | Setting both requires `AUTH PLAIN` or `AUTH LOGIN` |
| `SMTP_PASSWORD_FILE` | — | Read the SMTP password from this file instead |
| `SMTP_ALLOW_INSECURE_AUTH` | `true` on loopback, else `false` | Allow AUTH without STARTTLS |
| `SMTP_TLS_CERT_FILE` / `SMTP_TLS_KEY_FILE` | empty | Enable STARTTLS. Reloaded when either file changes. |
| `SMTP_ALLOW_UNAUTHENTICATED` | `false` | Allow a non-loopback listener without AUTH |
| `SMTP_ALLOWED_SENDER_DOMAINS` | organization of `MAILGUN_DOMAIN` | Comma-separated sender domains; `*.example.com` matches subdomains, `*` allows any sender |
| `SMTP_MAX_MESSAGE_BYTES` | `10485760` | Maximum message size |
| `SMTP_MAX_RECIPIENTS` | `50` | Maximum recipients per message |
| `SMTP_MAX_CONNECTIONS` | `100` | Maximum simultaneous SMTP sessions; further connections get `421` |
| `SMTP_AUTH_MAX_FAILURES` | `10` | Failed `AUTH` attempts per client address before it is blocked for 15 minutes; `0` disables (see [Security model](#security-model)) |
| `SMTP_READ_TIMEOUT` / `SMTP_WRITE_TIMEOUT` | `30s` | Per-command timeouts |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FILE` | empty | Also log to this file. Logs always go to stderr. |
| `HEALTH_LISTEN` | empty (`0.0.0.0:8080` in the image) | Address for `/health` and `/ready`; empty disables them (see [Health checks](#health-checks)) |
| `PASSPORT_ENV_FILE` | — | Env file to read at startup. It must exist. |

Mailgun is reached over HTTPS, through `HTTPS_PROXY` if that variable is set.

### Secrets in files

`MAILGUN_API_KEY_FILE` and `SMTP_PASSWORD_FILE` name a file that holds the secret, so it does not have to sit in the environment. Only the trailing line break is removed from the file. Setting both `MAILGUN_API_KEY` and `MAILGUN_API_KEY_FILE` (or both password variables) is an error.

With Podman secrets:

```bash
printf '%s' 'key-...' | podman secret create mailgun-api-key -
podman run -d --name mailgun-passport -p 127.0.0.1:587:2525 \
  --secret mailgun-api-key -e MAILGUN_API_KEY_FILE=/run/secrets/mailgun-api-key \
  -e MAILGUN_DOMAIN=mg.example.com --read-only --cap-drop=ALL \
  ghcr.io/devrfc83/mailgun-passport:latest
```

With systemd 250 or newer (every supported distribution except RHEL/Alma/Rocky 8), `LoadCredential=` reads root-only files, such as the API key or Let's Encrypt certificates, and hands the service a private copy. The `mailgun-passport` user then needs no access to the originals. Remove `MAILGUN_API_KEY` from the env file and run `sudo systemctl edit mailgun-passport`:

```ini
[Service]
LoadCredential=mailgun-api-key:/etc/mailgun-passport/mailgun-api-key
Environment=MAILGUN_API_KEY_FILE=%d/mailgun-api-key

# Optional: STARTTLS with certificates the service user cannot read.
LoadCredential=tls.crt:/etc/letsencrypt/live/mail.example.com/fullchain.pem
LoadCredential=tls.key:/etc/letsencrypt/live/mail.example.com/privkey.pem
Environment=SMTP_TLS_CERT_FILE=%d/tls.crt SMTP_TLS_KEY_FILE=%d/tls.key
```

The relay reloads its certificate when `SMTP_TLS_CERT_FILE` or `SMTP_TLS_KEY_FILE` changes, so pointing them straight at a renewed certificate needs no restart. Credentials, however, are copied only when the service starts: with `LoadCredential=`, restart it after renewing, for example with `certbot renew --deploy-hook "systemctl restart mailgun-passport"`.

## Send a test message

With [swaks](https://jetmore.org/john/code/swaks/):

```bash
swaks --server 127.0.0.1:587 --to dest@example.com --from you@YOUR_DOMAIN \
  --header "Subject: Passport" --body "Hello"
```

Add `--auth PLAIN --auth-user relay --auth-password change-me` when `SMTP_USERNAME`/`SMTP_PASSWORD` are set.
