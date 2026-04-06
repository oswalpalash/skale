# Contributing

## Scope

This repository is intentionally narrow.

Before sending a change, read [AGENTS.md](./AGENTS.md). The important project
constraints are:

- recommendation-first, not autonomous actuation by default
- replay-first, not real-time cleverness at the expense of replay fidelity
- conservative safety behavior and explicit suppression reasons
- one narrow v1 wedge around `Deployment` targets and replay-backed evaluation

## Development Setup

Requirements:

- Go 1.26
- `kubectl` for demo or controller validation work
- Docker if you want to build the controller image

Common commands:

```bash
make build
make test
make test-ci
make manifests
make docker-build IMAGE=ghcr.io/oswalpalash/skale-controller:dev
```

`make test-ci` matches the repository CI path and uses `CGO_ENABLED=0`.

## Before Opening a PR

Run:

```bash
make test-ci
make manifests
git diff --exit-code -- config/crd/bases
```

If you change shell scripts under `hack/`, also run:

```bash
bash -n hack/*.sh
```

## Pull Request Expectations

Changes should include:

- tests for happy path and failure modes when behavior changes
- docs updates when operator-facing behavior changes
- explicit limitations when a feature remains partial

Avoid broadening scope casually. If a change introduces a larger direction than
the current v1 wedge, open an issue first and document the tradeoff clearly.
