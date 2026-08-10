# GlusterFS volume plugin

## Overview
Docker volume plugin that provisions Docker volumes of a GlusterFS volume. The plugin runs in a container with FUSE.

## Prerequisites
- Docker 20.10+ with plugin support and BuildKit
- Linux host with FUSE, CAP_SYS_ADMIN, and /dev/fuse available
- GlusterFS servers reachable from the host
- Optional: local test cluster

## Project Structure & Module Organization
- Root Go sources (`main.go`, `driver.go`, `gfs.go`, `state.go`, `utils.go`) implement the Docker volume plugin logic.
- Container build assets live at the root (`Dockerfile`, `config.json`).
- Automation scripts are in `ci/` and `scripts/` (for integration testing and publishing).
- CI configuration lives in `.github/workflows/*.yml`.
- Makefile: build, image, plugin, push, test, clean

## Commands
- `make image`: builds the plugin image via Docker Buildx.
- `make plugin`: exports the image into `plugin/rootfs` and copies `config.json`.
- `make build`: creates the Docker plugin from `./plugin` (also disables/removes any existing plugin name).
- `make push`: pushes the plugin after a successful build.
- `make clean`: removes `plugin/rootfs`, `plugin/container.id`, and `bin/linux` artifacts.

Notes:
- Configure via make variables: registry, plugin, context, node, volume, servers, alias
- Override defaults: make test servers=server1,server2 volume=gv0

# API

    gfs:
        driver: glusterfs
        driver_opts:
            server: rpi5.lan,rpi4.lan,truenas.lan # comma-separated list of volfile-servers hosts
            volume: "configs/speed" # name of the gfs volume (volfile-id) + optionally path to subdirectory

## Development Workflow
- Build locally: make build
- Configure and enable plugin:
  - docker plugin set $(plugin)
  - docker plugin enable $(plugin)
- Create/list/mount docker volumes as usual
- For integration testing:
  - Assume existing localy available glusterfs cluster
  - `make test servers=gfs.lan volume=configs`

## Lint/Typecheck (local)
- Format: go fmt ./...
- Vet: go vet ./...
- Typecheck: go build ./...
- Suggested Make targets to add:
  - make lint: go fmt ./... && go vet ./...
  - make typecheck: go build ./...

## Testing
- Unit tests: none yet; recommended to add for driver logic by introducing interfaces to mock:
  - gfapi operations (Init, Mount, Stat, Mkdir, Readdir, Rmdir, Unlink)
  - exec calls for mount/umount
- Testing is currently integration-focused. The canonical test is `./ci/test-plugin.sh`.
- The script assumes Docker and Docker Compose; run it from the repo root.

## Multi-arch Builds
- Current flow targets `linux/amd64` (x86_64) and `linux/arm64`
- Use docker buildx and multi-arch base images
- Add a Makefile target for buildx (platforms=linux/amd64,linux/arm64)
- Verify gluster client base supports both architectures

## Coding Conventions
- Go modules; avoid hard-coded hosts/volumes
- No logging timestamps (outer runtime provides them)
- Guard shared maps and state with RWMutex
- Do not log secrets or sensitive paths
- Use standard Go formatting (`gofmt`); keep imports grouped and sorted.
- Prefer clear, descriptive names for exported structs and methods; keep helper functions unexported when possible.
- Follow existing filename patterns: lowercase, single-purpose files (for example, `state.go` for state handling).

## Troubleshooting
- Docker daemon/plugin logs: check your platform’s Docker logs
- Inspect plugin rootfs with runc under /var/run/docker/plugins/runtime-root/plugins.moby/
- Stale FUSE mounts: unmount and retry; ensure CAP_SYS_ADMIN and /dev/fuse are present

## CI
- PR validation checks formatting, vetting, tests, and amd64/arm64 builds.
- Pushes to `develop` publish an exact-SHA source image and update the mutable
  `latest-amd64` and `latest-arm64` managed-plugin references only while that
  SHA remains the branch head. A completed current publication maintains an
  auto-merge promotion pull request from `develop` to `master`.
- Pushes to `master` reserve immutable `1.<minor>.0` repository tags and publish
  matching immutable `1.<minor>.0-amd64` and `1.<minor>.0-arm64`
  managed-plugin references. Publication is serialized per branch without
  discarding queued builds.
- Publication validates each architecture-qualified managed-plugin artifact.
  It does not publish unqualified multi-architecture managed-plugin manifest
  tags; historical publications such as `1.1.0` remain unchanged.
- Promotion requires the `PAT_ACTIONS` secret and repository auto-merge. The
  `master` rules must require both `Develop plugin publication` and
  `Go validation`; automation does not bypass checks, reviews, or rules.
- Remote GitHub Actions use current stable major-version tags (`@vN`).
- See [`.github/workflows.md`](.github/workflows.md) for triggers, permissions,
  promotion settings, version reservation, publishing behavior, recovery, and
  troubleshooting.

## Supported Architectures
- x86_64
- arm64

## Configuration & Runtime Notes
- The plugin is configured via Docker plugin settings (for example, `GFS_SERVERS=server1,server2`).
