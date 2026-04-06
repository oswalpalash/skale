# Releasing

This repository does not yet publish automated tagged releases, but the codebase
now publishes controller images automatically from GitHub Actions.

## Build Metadata

The following linker variables are set by the `Makefile`:

- `github.com/oswalpalash/skale/internal/version.Version`
- `github.com/oswalpalash/skale/internal/version.Commit`
- `github.com/oswalpalash/skale/internal/version.BuildDate`

All shipped binaries expose `-version`.

## Local Release Build

```bash
make build VERSION=v0.1.0
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:v0.1.0 VERSION=v0.1.0
```

## Automatic Publishing

Pushes to `main` publish:

- `ghcr.io/oswalpalash/skale-controller:main`
- `ghcr.io/oswalpalash/skale-controller:sha-<commit>`

Tag pushes matching `v*` publish:

- `ghcr.io/oswalpalash/skale-controller:vX.Y.Z`
- `ghcr.io/oswalpalash/skale-controller:latest`

The release workflow also uploads versioned binaries to the GitHub Release.

## Pre-Release Checks

```bash
make test-ci
make manifests
git diff --exit-code -- config/crd/bases
bash -n hack/*.sh
```

## Publish Checklist

- tag the release commit intentionally
- build binaries with version metadata
- build and publish the controller image to GHCR
- update install docs if image name or registry changes
- keep release notes explicit about telemetry requirements, safety behavior, and
  replay caveats
