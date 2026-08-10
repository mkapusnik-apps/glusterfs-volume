# Installation and upgrade

Docker managed-plugin installation and upgrade use an architecture-qualified
plugin reference. Users must select the reference that matches the Docker host:

| Docker host architecture | Current reference | Stable 1.1.0 reference |
| --- | --- | --- |
| `linux/amd64` (x86_64) | `latest-amd64` | `1.1.0-amd64` |
| `linux/arm64` | `latest-arm64` | `1.1.0-arm64` |

Confirm the Docker host architecture before selecting a reference:

```console
docker info --format '{{.Architecture}}'
```

This explicit selection is the public compatibility contract. Docker Engine
29.6.2 does not install this managed plugin through the repository's
multi-architecture manifest because the top-level manifest does not expose the
managed-plugin configuration in the form Docker expects.

## Tag lifecycle

- `latest-amd64` and `latest-arm64` are mutable. A successful current
  publication from `develop` can move them to a newer source revision.
- `1.<minor>.0-amd64` and `1.<minor>.0-arm64` are immutable stable
  publications. Use an existing version such as `1.1.0`; a published stable
  reference is not moved to another source revision.

Publication creates managed-plugin packages only under architecture-qualified
tags. It does not create new unqualified managed-plugin manifest tags. The
historical unqualified `1.1.0` publication remains unchanged, but it is not a
supported installation or upgrade reference.

## Install the current publication

Install only the mutable current tag matching the host.

For an amd64 host:

```console
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:latest-amd64
```

For an arm64 host:

```console
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:latest-arm64
```

## Install a stable publication

Stable references use the immutable `1.<minor>.0-<architecture>` format.
Replace the example version only with an existing repository release version,
while retaining the host's architecture suffix.

For an amd64 host:

```console
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-amd64
```

For an arm64 host:

```console
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-arm64
```

The GHCR package must be public for installation without registry
authentication.

## Upgrade

Make the plugin eligible for upgrade by stopping workloads and resolving active
references as appropriate, then disable it, as required by Docker:

```console
docker plugin disable gfs:latest
```

An installation using the `gfs` alias is addressed locally as `gfs:latest`.
That local name is not the unsupported public repository tag. The remote target
reference must match the Docker host architecture.

Upgrade that alias to stable version 1.1.0 on amd64:

```console
docker plugin upgrade \
  --grant-all-permissions \
  --skip-remote-check \
  gfs:latest \
  ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-amd64
```

Upgrade that alias to stable version 1.1.0 on arm64:

```console
docker plugin upgrade \
  --grant-all-permissions \
  --skip-remote-check \
  gfs:latest \
  ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-arm64
```

After a successful upgrade, the plugin can be enabled again. If the wrong
architecture suffix is selected, the operation is not supported; retry with the
reference matching the host.

```console
docker plugin enable gfs:latest
```

The examples upgrade to immutable version `1.1.0`. To follow the mutable
current publication instead, use `latest-amd64` or `latest-arm64` as the remote
target, matching the host architecture.

## Unqualified repository tags

Unqualified repository tags such as `latest` and `1.1.0` are not supported
installation or upgrade references for the managed plugin. The presence of a
historical unqualified tag in GHCR does not promise that Docker Engine can
select a compatible managed-plugin artifact from it.

The historical `1.1.0` tag and its published artifacts remain immutable. This
contract does not republish, delete, or redirect that tag to work around Docker's
managed-plugin manifest handling.

## Verify the selected artifact

Record the exact architecture-qualified reference and digest used for an
installation or upgrade. Inspecting that registry reference with
`docker manifest inspect --verbose` reports its platform; it must match the
Docker host architecture.

The plugin startup entry in the Docker daemon log reports the embedded release
version and full source revision:

```text
Starting GlusterFS Volume Plugin version=<version> revision=<source-sha>
```

Together, the reference digest, reported platform, version, and revision
identify the installed artifact. Docker redirects managed-plugin output to its
daemon log; use the host's Docker logging facilities to locate the entry.
