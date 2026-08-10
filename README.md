# GlusterFS volume plugin

## Overview
Docker volume plugin that provisions Docker volumes of a GlusterFS volume. The plugin runs in a container with FUSE.

Runtime startup reconciliation, mount recovery, reference behavior, diagnostics, and
artifact provenance are documented in [Runtime recovery](docs/runtime-recovery.md).

## Installation

Docker managed plugins do not automatically select an artifact from this
repository's multi-architecture manifests. Install the architecture-qualified
reference that matches the Docker host.

The current `develop` publications are mutable:

```console
# linux/amd64 (x86_64)
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:latest-amd64

# linux/arm64
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:latest-arm64
```

For a stable deployment, install an immutable architecture-qualified release:

```console
# linux/amd64 (x86_64)
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-amd64

# linux/arm64
docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:1.1.0-arm64
```

Unqualified repository tags such as `latest` and `1.1.0` are not supported
managed-plugin installation or upgrade references. See
[Installation and upgrade](docs/installation.md) for architecture selection,
tag lifecycle, upgrade commands, and verification guidance.

The GHCR package must be public for these commands to work without registry
authentication.

## Usage

docker-compose.yml:

    volumes:
        gfs:
            driver: gfs
            driver_opts:
                server: glusterfs.server # comma-separated list of volfile-servers hosts
                volume: "volname/subdir" # name of the gfs volume (volfile-id) + optionally path to subdirectory
