# Local Go toolchain

Use `golang:1.20-bullseye` through the Bash tool and standard Docker CLI for local
Go, gofmt and Make commands. Keep the caller's numeric UID/GID, mount only this
repository at `/workspace`, and use a disposable writable HOME with `--rm --init`.
Use a unique container name, a 900-second in-container deadline, and a Bash timeout
of at least 930000 ms. Inspect/remove the exact task-owned container after cancellation.

Preserve `/app/.cache/go-build` and `/app/.cache/go-mod`. Mount only those cache
subdirectories at `/cache/go-build` and `/cache/go-mod`, and set `GOCACHE` and
`GOMODCACHE` to their respective container paths. Resolve volume subpaths or
Docker-host bind paths explicitly rather than inheriting all OpenCode mounts.

For Linux ARM64 cross-compilation, additionally set `CGO_ENABLED=0`, `GOARCH=arm64`,
and `GOOS=linux`. Native commands are suitable only when their version matches
this toolchain policy. No custom runtime manifest is required.
