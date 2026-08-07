# GitHub Actions workflows

## PR Validation

`workflows/validate.yml` runs for pull requests targeting `develop` or `master`.
Its single `Go validation` job checks formatting, runs `go vet` and the existing
Go test suite, builds natively on the hosted amd64 runner, and cross-builds for
`linux/arm64` with CGO disabled.

The workflow has read-only repository permission, persists no checkout token,
does not use secrets, and never publishes artifacts or packages. A newer run for
the same pull request cancels an older in-progress validation run.

## Publish Docker Plugin

`workflows/publish.yml` runs only after a push to `master`. It builds the
`linux/amd64` and `linux/arm64` source images, publishes an immutable source-SHA
image, packages architecture-specific Docker plugins, and publishes an annotated
multi-architecture plugin manifest.

Publishing requires the workflow-provided GitHub token with `packages: write`.
No repository secret is required. The immutable `source-<sha>` image retains OCI
source, revision, and version labels; the plugin binary reports its version and
revision during startup. Publish runs for `master` are serialized to prevent
concurrent updates of the mutable architecture and `latest` tags.

The source tag is retained for operators, but plugin packaging does not resolve
that tag. The build action's multi-platform index digest is inspected, exactly
one `linux/amd64` and one `linux/arm64` manifest digest are selected, and each
root filesystem is pulled and exported by its platform-specific digest.

All third-party and GitHub-maintained actions are pinned to their latest stable
major-version tag (`@vN`). This accepts compatible upstream updates within the
selected major automatically, including security fixes, but a movable major tag
does not provide the supply-chain immutability of a reviewed full commit SHA.
Major-version upgrades remain explicit repository changes.

## Troubleshooting

- A formatting failure includes the working-tree diff produced by `go fmt`.
- Vet, test, or build failures should be reproduced with the command shown by the
  failing step using Go 1.20.
- An arm64-only failure indicates a cross-platform compile issue; that step does
  not execute the arm64 binary.
- Publish failures should be checked for GHCR permission errors, QEMU or Buildx
  setup failures, and correct platform annotations on the final plugin manifest.
