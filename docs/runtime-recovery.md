# Runtime recovery

The plugin reconciles persisted managed volume definitions with the mount table before
opening its Docker plugin socket. Reconciliation is scoped to the deterministic target
for each volume under `/var/lib/glusterfs-volume`; it does not repair Gluster servers,
bricks, networking, or Swarm scheduling.

## Recovery guarantees and outcomes

For each valid definition in `volumes.json`, the plugin reads
`/proc/self/mountinfo` and reaches one of these outcomes before accepting requests:

| Detected state | Outcome |
| --- | --- |
| No mount | The mountpoint directory is prepared and the volume remains unmounted until requested. |
| One responsive `fuse.glusterfs` mount at the exact managed target | The physical mount is preserved and can be reused by the next successful request. |
| A disconnected or stale managed mount reporting `ENOTCONN`, `ESTALE`, or `EIO` | The mount is lazily detached. A later request creates and verifies a fresh mount. |
| Duplicate managed GlusterFS mounts at the exact target | All stacked duplicates are lazily detached so a later request can create one verified mount. |
| An incompatible object, unknown filesystem, nested mount, unreadable state, or failed recovery | The volume is blocked with a diagnostic. Other volumes and plugin requests continue to work. |

Every new physical mount is checked against mountinfo and probed as a directory before
the request succeeds. A successful mount command without exactly one responsive
physical mount is treated as a failure. Residual managed state from a failed attempt is
detached when it is safe to identify; logical state is not recorded for the failed
request, so the request remains retryable.

The same behavior applies to whole-volume and subdirectory-backed definitions. For a
subdirectory definition, the plugin temporarily mounts the volume root to ensure the
remote subdirectory exists, unmounts it, and then creates and verifies the requested
subdirectory mount.

## Mountpoint directories and local data

An existing ordinary directory is a valid mountpoint, whether it is empty or non-empty.
The plugin never deletes, truncates, or otherwise changes local entries in that
directory. It logs a warning before a mount hides non-empty local contents. Those
contents become visible again after unmounting.

An existing non-directory object is incompatible. The plugin preserves it and rejects
only requests for that volume until an operator moves or removes the object manually.
The plugin also preserves unknown or nested mounts rather than hiding or detaching
them.

## Logical references

All successful Docker `Mount` calls for a volume share one verified physical mount.
Each call adds one logical reference, including repeated calls with the same Docker
client ID. A matching `Unmount` removes one reference. An unknown client ID or an
unmount in excess of the successful mount count fails without changing valid
references or the physical mount.

The physical mount is released only with the last matching logical reference. If that
release fails, the final reference is preserved so Docker can retry truthfully. Logical
references are process-local; after a plugin restart they begin empty and Docker's
subsequent successful mount calls recreate them, while startup reconciliation handles
the surviving physical state.

## Unknown mounts

The ownership boundary is an exact target derived from a persisted volume name. A
`fuse.glusterfs` mount at that exact target is treated as managed recovery state.
Mounts at targets that have no persisted definition, mounts below a managed target, and
non-GlusterFS mounts at a managed target are unknown. The plugin warns about and
preserves unknown mounts. It rejects only requests whose target conflicts with them;
unrelated volumes continue to operate.

The plugin never scans, cleans, or unmounts paths outside its managed root.

## Diagnostics and operator action

Recovery failures identify:

- the persisted volume name and exact target;
- the detected condition;
- what recovery changed or preserved;
- the underlying operating-system or GlusterFS command cause; and
- the next manual action or retry step.

Diagnostics intentionally do not print environment contents or credentials. Server,
volume, and target identifiers may appear because they are necessary to identify the
affected resource. A volume's latest recovery problem is also exposed as
`recovery_issue` in the Docker volume status returned by `Get`.

Typical operator actions are to restore GlusterFS connectivity, inspect the target's
mount stack, or manually relocate an unknown conflicting mount. The plugin does not
delete local data or attempt infrastructure repair.

## Architectures

The recovery logic uses Linux mountinfo and standard FUSE unmount behavior and is built
for both `linux/amd64` (x86_64) and `linux/arm64` by the publishing workflow.

## Artifact provenance

Builds embed a version and full source revision in the binary. Startup output includes:

```text
Starting GlusterFS Volume Plugin version=<version> revision=<source-sha>
```

Published container images also expose `org.opencontainers.image.version`,
`org.opencontainers.image.revision`, and `org.opencontainers.image.source` labels.
The publishing workflow passes the GitHub source SHA as the revision for both
architectures. Local `make` builds default to the current Git revision and the output
of `git describe`; callers can override `VCS_REF` and `VERSION` explicitly.
