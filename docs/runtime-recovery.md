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
| One responsive `fuse.glusterfs` mount with the configured source identity at the exact managed target | The physical mount is preserved and can be reused by the next successful request. |
| A disconnected or stale managed mount reporting `ENOTCONN`, `ESTALE`, or `EIO` | The mount is lazily detached. A later request creates and verifies a fresh mount. |
| Duplicate mounts at the exact target | The ambiguous stack is preserved and the volume is blocked so recovery cannot disrupt healthy or unknown users. An operator must resolve the stack. |
| An incompatible object, unknown filesystem, nested mount, unreadable state, or failed recovery | The volume is blocked with a diagnostic. Other volumes and plugin requests continue to work. |
| An invalid persisted name, volume, subdirectory, server, or target | No target is constructed and no filesystem or mount operation is attempted for that definition. |

Every new physical mount is checked against mountinfo and probed as a directory before
the request succeeds. A successful mount command without exactly one responsive
physical mount is treated as a failure. Residual managed state from a failed attempt is
detached when it is safe to identify; logical state is not recorded for the failed
request, so the request remains retryable.

When a mount command leaves one identifiable mount but post-mount health verification
fails, rollback requires pre/post evidence that the mount ID was absent before the
attempt, the complete configured identity matches, and the record remains unchanged
immediately before detach. An unreadable post-command mount table, mismatched identity,
pre-existing mount ID, concurrent replacement, or duplicate record is preserved with an
actionable unavailable outcome. The plugin never uses a failed identity read as a reason
to unmount a sole record.

Mount identity includes the mount ID and parent, device, root, target, mount and super
options, filesystem type, and source retained from mountinfo. Reuse additionally
requires `fuse.glusterfs`, a read-write root, and the Gluster source derived from one of
the configured servers plus the exact volume and subdirectory. A whole-volume mount
left by interrupted subdirectory preparation therefore cannot be returned as the
configured subdirectory. If identity or ownership is ambiguous, the plugin preserves
the mount and fails only that volume with operator guidance.

Health checks run in a killable helper process with a three-second per-probe deadline.
Startup reconciliation has a 30-second overall deadline plus a bounded helper-process
termination allowance and completes, recording actionable unavailable outcomes for
unfinished volumes, before the plugin socket opens. Mount and unmount commands are also
context-bounded. A timeout never counts as proof of mount health.

The same behavior applies to whole-volume and subdirectory-backed definitions. For a
subdirectory definition, the plugin temporarily mounts the volume root to ensure the
remote subdirectory exists, unmounts it, and then creates and verifies the requested
subdirectory mount.

Remote subdirectory traversal and creation are bound to an open descriptor for the
verified temporary mount. The plugin checks the complete mount identity before opening,
checks the descriptor device against mountinfo, revalidates identity before mutation,
and uses descriptor-relative `openat` and `mkdirat` operations with symlink following
disabled. Every traversed component must remain a directory on the verified device. If
the temporary mount disappears or is replaced, no path-based mutation is attempted, so
the underlying ordinary mountpoint directory and unknown replacement remain unchanged.

## Mountpoint directories and local data

An existing ordinary directory is a valid mountpoint, whether it is empty or non-empty.
The plugin never deletes, truncates, or otherwise changes local entries in that
directory. It logs a warning before a mount hides non-empty local contents. Those
contents become visible again after unmounting.

An existing non-directory object is incompatible. The plugin preserves it and rejects
only requests for that volume until an operator moves or removes the object manually.
The plugin also preserves unknown or nested mounts rather than hiding or detaching
them.

Persisted definitions are validated before mountinfo matching, probing, directory
creation, mounting, or unmounting. Volume keys must resolve to one safe child of the
managed root; persisted names must match their map keys; and volume, subdirectory, and
server fields must be safe relative values. Invalid legacy or corrupt definitions are
reported but cannot cause inspection or mutation outside the managed root.

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

Every regular physical unmount retains the complete mountinfo record accepted before
the health probe, then reads mountinfo again immediately before invoking unmount. The
record must remain the sole exact mount, have no nested mounts, and match the accepted
identity completely. Missing, duplicated, replaced, changed, or unreadable state fails
closed without regular or lazy unmount. This applies to the final logical-reference
release, volume removal, and temporary whole-volume cleanup during subdirectory setup;
caller state and unknown replacement mounts remain available for inspection and retry.

## Unknown mounts

The ownership boundary is an exact, validated target derived from a persisted volume
name plus the expected GlusterFS source identity. A mount at the target with a different
volume, subdirectory, filesystem, root, or access mode is unknown. Mounts at targets
that have no valid persisted definition and mounts below a managed target are also
unknown. The plugin warns about and preserves unknown mounts. It rejects only requests
whose target conflicts with them; unrelated volumes continue to operate.

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
architectures. It first publishes the labeled image under the immutable
`source-<full SHA>` tag, then creates and validates the `linux/amd64` and
`linux/arm64` managed-plugin packages from that exact image. Docker managed-plugin
users install and upgrade through the architecture-qualified packages described in
[Installation and upgrade](installation.md); the publishing workflow does not create
new unqualified managed-plugin manifest tags. Local `make` builds default to the
current Git revision and the output of `git describe`; callers can override `VCS_REF`
and `VERSION` explicitly.
