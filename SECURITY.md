# Security policy

Mailgun Passport accepts mail from applications and sends it with your Mailgun API key. A flaw in it can expose that key, let someone send mail as your domain, or let a stranger use the relay. Please report such flaws privately.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability**
([direct link](https://github.com/devrfc83/mailgun-passport/security/advisories/new)). Please do not open a public issue or pull request for a vulnerability, and do not post it anywhere else before a fix is out.

A useful report has:

- the version (`mailgun-passport -version`) and how it is installed (package, container, source);
- the configuration that matters, with secrets removed;
- the steps or the SMTP conversation that show the problem, and what you expected instead;
- what you think an attacker gains.

This is a small project with one maintainer, so there is no guaranteed response time. Reports are read, answered and fixed as soon as that is possible, and you will be told when a fix is released. Reporters are credited in the release notes unless they prefer not to be.

## Supported versions

Only the latest release gets security fixes. Fixes ship as a new patch release, announced in [CHANGELOG.md](CHANGELOG.md), and the container tags `X.Y` and `latest` move to it. Upgrade rather than backport.

## What counts

In scope:

- using the relay without authorization: bypassing `AUTH`, the AUTH failure limit, `SMTP_ALLOWED_SENDER_DOMAINS` or `SMTP_RATE_LIMIT_PER_MINUTE`, or becoming an open relay under a configuration the README calls safe;
- reading or leaking secrets: the API key or SMTP password in logs, metrics, health endpoints or error replies sent to clients, or queue files readable by other users;
- injecting headers, recipients or Mailgun parameters that the client should not control (for example setting `o:testmode`, or adding a `Bcc` that bypasses the envelope);
- a crafted message or connection that crashes the relay, or uses memory or disk without the limits the settings promise;
- tampering with releases: a package, image or `SHA256SUMS` whose signature does not come from this repository's release workflow.

Out of scope:

- vulnerabilities in Mailgun's service, or in Go and the libraries in `go.mod` that the relay does not reach (`govulncheck` runs in CI, and Dependabot updates them weekly);
- a setup the documentation warns against: `SMTP_ALLOW_UNAUTHENTICATED=true` or `SMTP_ALLOW_INSECURE_AUTH=true` on an untrusted network, a published Docker port on a public interface, or a world-readable env file;
- attacks that need root, or the relay's own user, on the host;
- a single client using up its own allowance, such as filling the queue within `QUEUE_MAX_BYTES`.

## How it is built to resist

The [security model](README.md#security-model) in the README lists the defaults: loopback only, no open relay without an explicit setting, constant-time password comparison, AUTH limits per session and per address, restricted sender domains, and a hardened systemd unit and a distroless non-root image. Messages kept by the [queue](README.md#the-on-disk-queue) are readable only by the relay's user, and logs never include the API key, the SMTP password or message subjects at `info` level.

## Verifying a download

Every release is signed with Sigstore, with no long-lived key to steal. Check the packages and images before you run them, as shown in [Verify a download](README.md#verify-a-download).

## Hardening checklist

- Keep `SMTP_LISTEN` on `127.0.0.1` unless other hosts must connect. If they must, set `SMTP_USERNAME`/`SMTP_PASSWORD` with a long random password, configure STARTTLS, and firewall the port.
- Keep the Mailgun key out of the environment where you can: `MAILGUN_API_KEY_FILE` with systemd `LoadCredential=` or a container secret. Use a Mailgun *sending* key limited to one domain, not the account's primary key.
- Set `SMTP_RATE_LIMIT_PER_MINUTE` so a runaway application cannot spend your quota, and alert on `mailgun_passport_smtp_auth_attempts_total{result="failure"}`.
- Keep `HEALTH_LISTEN` and `/metrics` on a private network.
- Update. Subscribe to the repository's releases.
