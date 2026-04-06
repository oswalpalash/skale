# Releasing

This repository does not yet publish automated tagged releases, but the codebase
now has the minimum plumbing needed for versioned binaries and a controller
image.

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
