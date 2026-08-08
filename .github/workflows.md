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

`workflows/publish.yml` runs after pushes to `develop` and `master`. It publishes
only to `ghcr.io/mkapusnik-apps/glusterfs-volume`. Each run builds the
`linux/amd64` and `linux/arm64` source images and publishes them under a
SHA-addressed `source-<sha>` tag. Current publication runs then package
architecture-specific Docker plugins and publish an annotated multi-architecture
plugin manifest.

`develop` updates the intentionally mutable `latest`, `latest-amd64`, and
`latest-arm64` plugin tags. `master` reserves and publishes an immutable
`1.<minor>.0` set consisting of the final tag and matching `-amd64` and `-arm64`
tags. The first version is `1.0.0` when no exact, canonical `1.<minor>.0`
repository tag exists. Each subsequent master run increments the greatest valid
minor.

The master version is reserved before the image build by atomically creating a
lightweight repository Git tag at the workflow's exact build SHA. Existing
version tags are never updated or overwritten. A create collision is verified,
all version refs are fetched again, and allocation retries with the next minor;
ambiguous or failed API operations stop publication. A failed run after
reservation can therefore consume a version without publishing all plugin tags.
These intentional version gaps preserve immutable durable state and must not be
filled by moving or reusing a tag.

Publishing requires the workflow-provided GitHub token with `contents: write` to
reserve master version tags and `packages: write` to publish to GHCR. No
repository secret is required. The build action applies OCI title, source, exact
revision, version, and source-tag reference labels independently of Dockerfile
support. `VERSION` and `VCS_REF` are also passed as compatibility build arguments
for Dockerfiles that consume them; publication does not depend on those arguments
and the workflow makes no guarantee about binary-visible version output.

Runs are serialized independently per publication branch, with queueing enabled
so newer pushes do not replace already queued builds. GitHub currently retains up
to 100 queued runs per concurrency group. Because GitHub does not guarantee
dispatch order, a queued `develop` run rechecks the live branch head after its
SHA-addressed image build and skips mutable plugin publication when superseded.
This prevents an older build from overwriting `latest` while still retaining the
run and its source image. `develop` and immutable `master` releases use separate
queues and can proceed independently.

Before a master build pushes plugin packages, it verifies that the selected final
and architecture tags do not already exist. An existing tag, registry permission
failure, or ambiguous registry response fails closed rather than risking an
overwrite. `latest` intentionally skips this protection.

The `source-<sha>` tag is retained for operators, but it remains mutable like any
registry tag and plugin packaging does not resolve it. The immutable identity is
the build-produced multi-platform index digest. That index is inspected, exactly
one `linux/amd64` and one `linux/arm64` child digest are selected, and each root
filesystem is pulled and exported by its platform-specific digest.

All third-party and GitHub-maintained actions are pinned to their latest stable
major-version tag (`@vN`). This accepts compatible upstream updates within the
selected major automatically, including security fixes, but a movable major tag
does not provide the supply-chain immutability of a reviewed full commit SHA.
Major-version upgrades remain explicit repository changes.

## GHCR package settings

GHCR package visibility and repository access cannot be declared in workflow
files. After the first publication, an organization owner must open the package
settings for `mkapusnik-apps/glusterfs-volume` and set visibility to **Public** if
the unauthenticated install command in the README is intended to work. If the
package does not inherit access from its linked repository, its **Manage Actions
access** list must grant `mkapusnik-apps/glusterfs-volume` **Write** access before
publication can succeed.

## Troubleshooting

- A formatting failure includes the working-tree diff produced by `go fmt`.
- Vet, test, or build failures should be reproduced with the command shown by the
  failing step using Go 1.20.
- An arm64-only failure indicates a cross-platform compile issue; that step does
  not execute the arm64 binary.
- Publish failures should be checked for GHCR permission errors, QEMU or Buildx
  setup failures, version-tag reservation errors, immutable-tag preflight errors,
  and correct platform annotations on the final plugin manifest.
- Never move an existing `1.<minor>.0` Git tag or reuse a version left by a failed
  master run. Resolve API or package permission failures and let the next run
  reserve the next minor.
