# Building Mailgun Passport

This document covers running from source, building binaries, packages and container images, and publishing a release. To install and configure a released version, see the [README](README.md).

## Requirements

- Go 1.25 or newer, for everything except `make container-packages`
- Podman or Docker, for `make image`, `make container-packages` and `make test-packages`
- git, since versions come from tags

No `rpmbuild`, `dpkg-buildpackage` or distribution Go toolchain is involved: packages are built with [nFPM](https://nfpm.goreleaser.com), which `go run` fetches at a pinned version.

## Run from source

```bash
cp .env.example .env    # set MAILGUN_API_KEY and MAILGUN_DOMAIN
PASSPORT_ENV_FILE=.env SMTP_LISTEN=127.0.0.1:2525 go run ./cmd/mailgun-passport

swaks --server 127.0.0.1:2525 --to dest@example.com --from you@YOUR_DOMAIN \
  --header "Subject: Passport" --body "Hello"
```

The default port, 587, is privileged, so an unprivileged user cannot bind it. Use a port above 1024 during development, as above, or set `SMTP_LISTEN` in `.env`. The relay does not read `.env` on its own: `PASSPORT_ENV_FILE` has to name it.

Set `MAILGUN_TESTMODE=true` to have Mailgun accept messages without delivering them.

## Install with go install

From v0.2.0 on, the module path is `github.com/devrfc83/mailgun-passport`:

```bash
go install github.com/devrfc83/mailgun-passport/cmd/mailgun-passport@latest
```

This builds only the binary. The systemd unit, the system user and the env file come with the packages (see the [README](README.md)). `mailgun-passport -version` reports the module version, without commit or build date.

## Make targets

| Target | Result |
| --- | --- |
| `make build` | `dist/mailgun-passport` for this machine |
| `make test` | `go vet` and the test suite with the race detector |
| `make packages` | `.rpm` and `.deb` for `ARCHES` (default `amd64 arm64`) in `dist/` |
| `make rpm` / `make deb` | a single package format |
| `make container-packages` | the same packages built inside `golang:1.27`; only Podman or Docker is needed |
| `make test-packages` | installs, checks and removes the amd64 packages in 15 distribution containers |
| `make image` | the container image `mailgun-passport:<version>` and `:latest` |
| `make clean` | removes `dist/` |

`make help` lists them with the detected version and container runtime. Podman is preferred when both are installed; set `CONTAINER=docker` to override.

## Versions

Versions come from `vX.Y.Z` git tags (see [scripts/version.sh](scripts/version.sh)):

| Checkout | Version |
| --- | --- |
| a tagged commit, clean tree | `X.Y.Z` |
| N commits after the tag | `X.Y.Z+N.gHASH` |
| uncommitted changes | `X.Y.Z+N.gHASH.dirty` |

The `+` suffix sorts after the release in both rpm and dpkg, so development builds upgrade cleanly. Override with `make packages VERSION=1.2.3`. Container image tags cannot contain `+`, so `make image` replaces it with `-`. The binary embeds the version, commit and build date; `mailgun-passport -version` prints them.

## Container image

```bash
make image                  # podman, or docker with CONTAINER=docker
```

The Dockerfile cross-compiles in the build stage, so `--platform linux/arm64` does not need emulation. The runtime stage is `distroless/static` running as UID 65532 and listening on port 2525.

The Quadlet unit in [contrib/podman/](contrib/podman/mailgun-passport.container) uses `localhost/mailgun-passport:latest` by default, which is what `make image` produces. Build the image as the same user that runs the unit (root for `/etc/containers/systemd/`).

## Continuous integration

[.github/workflows/ci.yml](.github/workflows/ci.yml) runs on every push to `main` and every pull request: `gofmt`, `go mod tidy`, `make test`, `make build` and `govulncheck`.

## Releasing

Pushing a `vX.Y.Z` tag runs [.github/workflows/release.yml](.github/workflows/release.yml):

1. `make test`
2. `make packages` and `make test-packages`, after checking that `scripts/version.sh` matches the tag
3. a multi-arch image (amd64 and arm64), with SBOM and provenance, pushed to GHCR and Docker Hub
4. a GitHub release with the `.rpm` and `.deb` files and `SHA256SUMS`. It is created only after steps 2 and 3 succeed. Tags with a suffix such as `v1.0.0-rc1` become prereleases and do not move `latest`.

```bash
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

One-time setup in GitHub, under Settings → Secrets and variables → Actions:

| Kind | Name | Value |
| --- | --- | --- |
| Variable | `DOCKERHUB_NAMESPACE` | the Docker Hub organization |
| Secret | `DOCKERHUB_TOKEN` | an organization access token with read/write access to `<namespace>/mailgun-passport` |
| Variable | `DOCKERHUB_USERNAME` | optional. Only needed with a personal access token: the Docker Hub user that owns it. Defaults to `DOCKERHUB_NAMESPACE`. |

The GHCR package is created private on the first push. Make it public under the package settings on GitHub.
