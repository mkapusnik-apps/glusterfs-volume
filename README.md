# GlusterFS volume plugin

## Overview
Docker volume plugin that provisions Docker volumes of a GlusterFS volume. The plugin runs in a container with FUSE.

Runtime startup reconciliation, mount recovery, reference behavior, diagnostics, and
artifact provenance are documented in [Runtime recovery](docs/runtime-recovery.md).

## Installation

Install the current `develop` publication through the intentionally mutable
`latest` tag:

    docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:latest

For a stable deployment, install an immutable `1.<minor>.0` publication by
replacing the example version with an existing repository release tag:

    docker plugin install --grant-all-permissions --alias gfs ghcr.io/mkapusnik-apps/glusterfs-volume:1.0.0

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
