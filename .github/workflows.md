# GitHub Actions workflows

## PR validation

`workflows/validate.yml` runs for pull requests targeting `develop` or `master`.
Its `Go validation` job checks formatting, runs `go vet` and the existing Go
test suite, builds natively on the hosted amd64 runner, and cross-builds for
`linux/arm64` with CGO disabled.

The workflow has read-only repository permission, persists no checkout token,
does not use secrets, and never publishes artifacts or packages. A newer run
for the same pull request cancels an older in-progress validation run.

## Develop publication and promotion

`workflows/publish-develop.yml` runs only after pushes to `develop`. The
`Develop plugin publication` job builds the exact pushed commit for
`linux/amd64` and `linux/arm64` and publishes its source index as
`ghcr.io/mkapusnik-apps/glusterfs-volume:source-<full-sha>`. If the pushed SHA
is still the live `develop` head immediately before mutable publication, the
job packages and updates `latest-amd64`, `latest-arm64`, and their annotated
multi-architecture `latest` manifest. A queued run whose SHA is already stale
keeps its exact-SHA source artifact but skips all `latest` tags.

The job checks the live head again after all three mutable tags are complete.
Only a successful publication that remains current can run the promotion job.
That job checks the head again before PR operations, does nothing when
`develop` has no commits to promote to `master`, and creates or reuses only the
exact open `develop` to `master` pull request. Its title is
`Promote develop to master`; its body is refreshed with the full successfully
published SHA. Closed or merged PRs are not reused, and an existing draft or
ambiguous duplicate open PR fails visibly instead of being made promotable.

The promotion job uses `secrets.PAT_ACTIONS`, not `GITHUB_TOKEN`, so PR-created
and PR-synchronize events start validation autonomously. The token must be able
to read repository contents, create and update repository pull requests, and
enable pull request auto-merge. Store only the token value in the repository
Actions secret named `PAT_ACTIONS`.

Promotion enables auto-merge with the merge-commit method. It never uses an
administrator merge or bypasses checks, reviews, merge queues, or other branch
rules. Reruns update the same exact-pair PR and accept merge-commit auto-merge
that is already enabled. A PR already configured with another auto-merge method
fails visibly. A create race is recovered only when exactly one matching open
non-draft PR can be proved. Failure to create or update the PR, or to enable and
verify auto-merge, fails the workflow after publication; it does not delete or
roll back the published plugin. Correct the token, repository setting, draft,
or API problem and rerun the failed workflow.

### Required repository settings

Repository administrators must enable **Allow auto-merge** and keep the
**Create a merge commit** merge method available. The rule protecting
`master` must require these exact GitHub Actions check names:

- `Develop plugin publication`
- `Go validation`

The publication context is intentionally a stable job name and is attached to
the exact `develop` commit built by the push workflow. Requiring it prevents an
already auto-merge-enabled promotion PR from merging after `develop` advances
to a newer, not-yet-published SHA. `Go validation` supplies the independent PR
validation gate. Required reviews and other existing repository or organization
rules remain in force.

`PAT_ACTIONS`, auto-merge, merge commits, and a required status context written
by name through the branch protection or rules API can be configured before
these workflow files merge. The GitHub settings UI may not offer
`Develop plugin publication` until that check has run once. If so, add it by
API before merging this change, or add it immediately after the first develop
publication and do not allow the generated promotion PR to merge first. The
API configuration is safely fail-closed before the first check exists: the
promotion cannot merge until the exact head supplies it.

After auto-merge completes, verify that `master` points to the expected merge
commit, its parents include the full published develop SHA, the master
publication workflow reserved a new repository `1.<minor>.0` tag at that merge
commit, and the matching final, `-amd64`, and `-arm64` GHCR plugin tags exist.

## Master publication

`workflows/publish-master.yml` runs only after pushes to `master` and never
updates `latest`. Before building, it atomically reserves an immutable
`1.<minor>.0` repository Git tag at the exact master SHA. The first version is
`1.0.0` when no exact canonical tag exists; each subsequent run increments the
greatest valid minor. It then publishes the exact-SHA source image and matching
versioned final, `-amd64`, and `-arm64` plugin tags.

Reservation collisions are verified, all version refs are fetched again, and
allocation retries with the next minor. Ambiguous or failed API operations stop
publication. Existing plugin tags also fail the immutable preflight closed
rather than being overwritten. A failure after reservation can therefore leave
an intentional version gap. Never move or reuse that repository tag; resolve
the failure and rerun so the next minor is reserved.

The master workflow uses `GITHUB_TOKEN` with `contents: write` for version tag
reservation and `packages: write` for GHCR. It does not use `PAT_ACTIONS` and
does not create pull requests.

## Publication implementation and concurrency

Both publication workflows publish only to
`ghcr.io/mkapusnik-apps/glusterfs-volume`. The shared local
`.github/actions/publish-plugin` composite action resolves the build-produced
multi-platform index digest, requires exactly one Linux child for each supported
architecture, exports those root filesystems, publishes architecture plugin
packages, and creates the final annotated plugin manifest. Plugin packaging
uses digests rather than resolving the mutable source tag.

Develop and master use stable, separate concurrency groups. `queue: max` keeps
up to 100 waiting runs in each group instead of replacing pending runs; GitHub
does not guarantee queue dispatch order. The develop live-head checks prevent a
run that is stale when it reaches a mutable or promotion gate from changing
`latest` or promotion state. Master versions remain safe under any dispatch
order because reservation and destination tags are immutable and fail closed.

The `source-<sha>` tag is an operator-facing exact-SHA reference but, like any
registry tag, is technically mutable. Its immutable build identity is the
build-produced index digest. OCI title, source, exact revision, version, and
source-tag labels are applied independently of Dockerfile support; `VERSION`
and `VCS_REF` are also supplied as compatibility build arguments.

The shared `mkapusnik-apps/commons/pull-request@v1` action is intentionally not
used for promotion. Its current implementation updates existing draft PRs,
does not provide the required no-diff no-op, and invokes the auto-merge mutation
without first handling already-enabled auto-merge. The repository script
implements only the stricter exact-pair promotion behavior.

All remote actions use their current stable major-version tag (`@vN`). This
accepts compatible upstream updates within a major, including security fixes,
but does not have the supply-chain immutability of a reviewed commit SHA. Major
upgrades remain explicit repository changes.

## GHCR package settings

GHCR package visibility and repository access cannot be declared in workflow
files. After the first publication, an organization owner must set the
`mkapusnik-apps/glusterfs-volume` package to **Public** if the unauthenticated
README installation commands should work. If the package does not inherit
access from its linked repository, **Manage Actions access** must grant this
repository **Write** access before publication can succeed.

## Troubleshooting

- A formatting failure includes the working-tree diff produced by `go fmt`.
- Vet, test, or build failures should be reproduced with the command shown by
  the failing step using Go 1.20. An arm64 build does not execute the binary.
- Develop source publication with no `latest` update means the run lost a
  live-head check. Inspect the newer queued develop run instead of rerunning the
  stale SHA.
- A promotion failure leaves published artifacts intact. Check `PAT_ACTIONS`,
  repository auto-merge and merge-commit settings, the exact open PR's draft
  and auto-merge state, and the required master checks before rerunning.
- Publish failures should be checked for GHCR access, QEMU or Buildx setup,
  version reservation, immutable-tag preflight, and final manifest platform
  annotations.
- Never fill a master version gap by moving an existing `1.<minor>.0` tag.
