# Contributing

Thank you for helping. Mailgun Passport is a small relay with a narrow job, so the best contributions keep it small, predictable and safe. For a vulnerability, do not open an issue: see [SECURITY.md](SECURITY.md).

## Before you start

For anything larger than a fix, open an issue first and say what problem you have. The project says no to some good ideas on purpose: it has **one SMTP user by design**, runs on Linux, and has a short list of dependencies. A pull request that adds a dependency, a setting or a behavior change should explain why the existing options do not cover it.

## Set up

You need Go 1.27 or newer (see [BUILD.md](BUILD.md#go-version)) and git. Podman or Docker is needed only for images and package tests.

```bash
git clone https://github.com/devrfc83/mailgun-passport && cd mailgun-passport
make test      # go vet and the tests, with the race detector
make cover     # the same, with coverage; fails below 90%
make lint      # staticcheck and golangci-lint, at pinned versions
make fuzz      # the parser fuzz targets, 30 seconds each (FUZZTIME=5m for longer)
```

`make help` lists the rest. To run the relay against Mailgun without delivering anything, see [Run from source](BUILD.md#run-from-source) and set `MAILGUN_TESTMODE=true`.

## What a good change looks like

- **Tests.** Every behavior change comes with a test, and a bug fix with one that fails without the fix. Tests use a fake Mailgun (`httptest`) and real SMTP over loopback; none needs the network or a Mailgun account. Run them with `-race`: `make test` does.
- **Parser changes.** [internal/parser](internal/parser) handles untrusted input. Run `make fuzz` after touching it. If a fuzz run saves a failing input under `testdata/fuzz/`, commit it with the fix.
- **Settings.** A setting is read in [internal/config/config.go](internal/config/config.go), which reports invalid values and never falls back silently. Give a new field its `env` tag, and `reload:"hot"` only if it can change while the relay runs without a race or a surprise (otherwise it needs a restart, and the reload code says so). Document it in the table in [README.md](README.md#configuration), in [.env.example](.env.example) and, if it matters in containers, in [README.docker.md](README.docker.md).
- **Metrics.** New counters go in [internal/metrics](internal/metrics), named `mailgun_passport_*`, with few and fixed label values (never an address, a subject or an error text) and documented in the README table.
- **Logging.** Never log the API key, the SMTP password or a message body. Subjects only at `debug`. Add context as `slog` attributes, not formatted into the message.
- **Comments say why.** The code explains the reason for a decision, such as why a status maps to `451`, not what each line does. Follow the style around you.
- **Dependencies.** Prefer the standard library. A new module needs a reason in the pull request. Keep `go.mod` tidy (`go mod tidy`); CI checks it.
- **Formatting.** `gofmt`. CI checks it.

## Changelog

Every change a user could notice gets a line in [CHANGELOG.md](CHANGELOG.md), under the section of the next release (`## vX.Y.Z`), in plain sentences, under *Added*, *Changed*, *Fixed* or *Security*. The release workflow turns that section into the release notes and fails if it is missing. Say what changes for the user and what they must do, if anything: `QUEUE_DIR` is new, `.env` is no longer read.

## Pull requests

- Branch from `main`, keep a pull request to one subject, and write commit messages in the imperative (`Limit failed AUTH attempts per client address`), with the reason in the body when it is not obvious.
- CI must pass: formatting, `go mod tidy`, `make lint`, `make test`, `make cover`, a short fuzz run and `govulncheck`.
- Do not bump versions or create tags in a pull request: the maintainer releases (see [Releasing](BUILD.md#releasing)).
- By contributing, you agree that your work is under the project's [MIT license](LICENSE).

## Reporting bugs

Open an issue with the version (`mailgun-passport -version`), how you run it, the relevant settings with secrets removed, and the log lines around the problem (`LOG_LEVEL=debug` shows more, subjects included: remove what is private). For mail that was delivered wrongly, the SMTP conversation (`swaks -tls --show-raw-text …`) helps most.
