package plugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const mountInfoPath = "/proc/self/mountinfo"
const internalMountProbeArgument = "--internal-mount-health-probe"
const startupRecoveryTimeout = 30 * time.Second
const requestOperationTimeout = 60 * time.Second
const defaultMountProbeTimeout = 3 * time.Second
const rollbackOperationTimeout = 10 * time.Second

const (
	probeExitHealthy = iota
	probeExitNotConnected
	probeExitStale
	probeExitIO
	probeExitOther
)

type mountInfoReader interface {
	read(context.Context) ([]mountRecord, error)
}

type mountHealthProbe interface {
	probe(context.Context, string) error
}

type procMountInfoReader struct {
	path string
}

type subprocessMountHealthProbe struct {
	timeout time.Duration
}

type mountRecord struct {
	mountID        int
	parentID       int
	majorMinor     string
	root           string
	target         string
	mountOptions   string
	optionalFields string
	filesystem     string
	source         string
	superOptions   string
}

type mountAttemptEvidence struct {
	preMountIDs map[int]struct{}
	state       volumeState
	subdir      string
}

type attemptMount struct {
	record mountRecord
}

type recoveryFailure struct {
	volume     string
	target     string
	condition  string
	result     string
	cause      error
	nextAction string
}

func (e *recoveryFailure) Error() string {
	return fmt.Sprintf(
		"volume %q target %q: detected %s; recovery result: %s; cause: %v; next action: %s",
		e.volume, e.target, e.condition, e.result, e.cause, e.nextAction,
	)
}

func (e *recoveryFailure) Unwrap() error {
	return e.cause
}

func newRecoveryFailure(volumeName, target, condition, result string, cause error, nextAction string) error {
	return &recoveryFailure{
		volume:     volumeName,
		target:     target,
		condition:  condition,
		result:     result,
		cause:      cause,
		nextAction: nextAction,
	}
}

func (r procMountInfoReader) read(ctx context.Context) ([]mountRecord, error) {
	file, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("open mount information: %w", err)
	}
	defer file.Close()

	var records []mountRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("read mount information: %w", err)
		}
		record, err := parseMountInfoLine(scanner.Text())
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read mount information: %w", err)
	}
	return records, nil
}

func parseMountInfoLine(line string) (mountRecord, error) {
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		return mountRecord{}, fmt.Errorf("invalid mountinfo entry %q", line)
	}
	left := strings.Fields(parts[0])
	right := strings.Fields(parts[1])
	if len(left) < 6 || len(right) < 3 {
		return mountRecord{}, fmt.Errorf("incomplete mountinfo entry %q", line)
	}
	mountID, err := strconv.Atoi(left[0])
	if err != nil {
		return mountRecord{}, fmt.Errorf("invalid mount ID %q: %w", left[0], err)
	}
	parentID, err := strconv.Atoi(left[1])
	if err != nil {
		return mountRecord{}, fmt.Errorf("invalid parent mount ID %q: %w", left[1], err)
	}
	root, err := unescapeMountInfoField(left[3])
	if err != nil {
		return mountRecord{}, fmt.Errorf("decode mount root %q: %w", left[3], err)
	}
	target, err := unescapeMountInfoField(left[4])
	if err != nil {
		return mountRecord{}, fmt.Errorf("decode mount target %q: %w", left[4], err)
	}
	source, err := unescapeMountInfoField(right[1])
	if err != nil {
		return mountRecord{}, fmt.Errorf("decode mount source %q: %w", right[1], err)
	}
	return mountRecord{
		mountID:        mountID,
		parentID:       parentID,
		majorMinor:     left[2],
		root:           root,
		target:         target,
		mountOptions:   left[5],
		optionalFields: strings.Join(left[6:], " "),
		filesystem:     right[0],
		source:         source,
		superOptions:   strings.Join(right[2:], " "),
	}, nil
}

func unescapeMountInfoField(value string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) {
			return "", errors.New("truncated escape")
		}
		decoded, err := strconv.ParseUint(value[index+1:index+4], 8, 8)
		if err != nil {
			return "", fmt.Errorf("invalid escape: %w", err)
		}
		result.WriteByte(byte(decoded))
		index += 3
	}
	return result.String(), nil
}

func recordsForTarget(records []mountRecord, target string) (exact []mountRecord, descendants []mountRecord) {
	target = filepath.Clean(target)
	prefix := target + string(os.PathSeparator)
	for _, record := range records {
		recordTarget := filepath.Clean(record.target)
		switch {
		case recordTarget == target:
			exact = append(exact, record)
		case strings.HasPrefix(recordTarget, prefix):
			descendants = append(descendants, record)
		}
	}
	return exact, descendants
}

func (r mountRecord) sameIdentity(other mountRecord) bool {
	return r.mountID == other.mountID &&
		r.parentID == other.parentID &&
		r.majorMinor == other.majorMinor &&
		r.root == other.root &&
		r.target == other.target &&
		r.mountOptions == other.mountOptions &&
		r.optionalFields == other.optionalFields &&
		r.filesystem == other.filesystem &&
		r.source == other.source &&
		r.superOptions == other.superOptions
}

func newMountAttemptEvidence(records []mountRecord, state volumeState, subdir string) mountAttemptEvidence {
	ids := make(map[int]struct{}, len(records))
	for _, record := range records {
		ids[record.mountID] = struct{}{}
	}
	return mountAttemptEvidence{preMountIDs: ids, state: state, subdir: subdir}
}

func (e mountAttemptEvidence) attributes(record mountRecord) bool {
	if _, existed := e.preMountIDs[record.mountID]; existed {
		return false
	}
	return recordMatchesExpected(record, e.state, e.subdir)
}

func expectedMountSources(state volumeState, subdir string) []string {
	sources := make([]string, 0, len(state.Servers))
	for _, server := range state.Servers {
		source := server + ":" + state.Volume
		if subdir != "" {
			source += "/" + subdir
		}
		sources = append(sources, source)
	}
	return sources
}

func recordMatchesExpected(record mountRecord, state volumeState, subdir string) bool {
	if record.filesystem != "fuse.glusterfs" || record.root != "/" || !hasMountOption(record.mountOptions, "rw") || !hasMountOption(record.superOptions, "rw") {
		return false
	}
	for _, source := range expectedMountSources(state, subdir) {
		if record.source == source {
			return true
		}
	}
	return false
}

func hasMountOption(options, expected string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == expected {
			return true
		}
	}
	return false
}

func (p subprocessMountHealthProbe) probe(parent context.Context, target string) error {
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultMountProbeTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/exe", internalMountProbeArgument, target)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("mount health probe exceeded %s: %w", timeout, ctx.Err())
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("run mount health probe: %w", err)
	}
	switch exitErr.ExitCode() {
	case probeExitNotConnected:
		return syscall.ENOTCONN
	case probeExitStale:
		return syscall.ESTALE
	case probeExitIO:
		return syscall.EIO
	default:
		return fmt.Errorf("mount health probe exited with status %d", exitErr.ExitCode())
	}
}

func runInternalMountProbe(target string) int {
	if !isLexicallyBeneath(propagatedMount, target) {
		return probeExitOther
	}
	err := probeMountedDirectory(target)
	switch {
	case err == nil:
		return probeExitHealthy
	case errors.Is(err, syscall.ENOTCONN):
		return probeExitNotConnected
	case errors.Is(err, syscall.ESTALE):
		return probeExitStale
	case errors.Is(err, syscall.EIO):
		return probeExitIO
	default:
		return probeExitOther
	}
}

func probeMountedDirectory(target string) error {
	directory, err := os.Open(target)
	if err != nil {
		return err
	}
	defer directory.Close()
	_, err = directory.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func isStaleMountError(err error) bool {
	return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ESTALE) || errors.Is(err, syscall.EIO)
}

func (d *glusterfsDriver) reconcileStartup() {
	ctx, cancel := context.WithTimeout(context.Background(), startupRecoveryTimeout)
	defer cancel()
	d.reconcileStartupContext(ctx)
}

func (d *glusterfsDriver) reconcileStartupContext(ctx context.Context) {
	d.Lock()
	defer d.Unlock()

	for name, state := range d.volumes {
		target, err := d.validatedTarget(name, state)
		if err != nil {
			d.recoveryIssues[name] = err
			log.Printf("Startup recovery blocked: %v", err)
			continue
		}
		if err := ctx.Err(); err != nil {
			recoveryErr := newRecoveryFailure(
				name, target, "startup recovery deadline reached", "performed no operation for this volume",
				err, "reduce blocked mount operations or resolve earlier recovery failures, then restart",
			)
			d.recoveryIssues[name] = recoveryErr
			log.Printf("Startup recovery blocked: %v", recoveryErr)
			continue
		}
		mounted, err := d.reconcileTarget(ctx, name, target, state)
		if err != nil {
			d.recoveryIssues[name] = err
			log.Printf("Startup recovery blocked: %v", err)
			continue
		}
		delete(d.recoveryIssues, name)
		if mounted {
			d.mounts[name] = &activeMount{
				mountpoint: target,
				createdAt:  time.Now().UTC(),
				ids:        map[string]int{},
			}
			log.Printf("Startup recovery volume %q target %q: detected one healthy mount with the configured identity; recovery result: preserved for request reuse", name, target)
			continue
		}
		if err := d.prepareMountpoint(name, target, state); err != nil {
			d.recoveryIssues[name] = err
			log.Printf("Startup recovery blocked: %v", err)
			continue
		}
		log.Printf("Startup recovery volume %q target %q: recovery result: ready without a physical mount", name, target)
	}

	d.warnUnknownMounts(ctx)
}

func (d *glusterfsDriver) warnUnknownMounts(ctx context.Context) {
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		log.Printf("Startup recovery could not scan for unknown mounts under %q: %v; next action: inspect mountinfo before using a conflicting target", d.root, err)
		return
	}
	known := make(map[string]struct{}, len(d.volumes))
	for name, state := range d.volumes {
		target, err := d.validatedTarget(name, state)
		if err == nil {
			known[target] = struct{}{}
		}
	}
	root := filepath.Clean(d.root)
	prefix := root + string(os.PathSeparator)
	for _, record := range records {
		target := filepath.Clean(record.target)
		if target == root || !strings.HasPrefix(target, prefix) {
			continue
		}
		if _, ok := known[target]; ok {
			continue
		}
		log.Printf("Startup recovery preserved unknown mount target %q filesystem %q; next action: avoid a conflicting managed volume name or resolve the mount manually", target, record.filesystem)
	}
}

func (d *glusterfsDriver) reconcileTarget(ctx context.Context, volumeName, target string, state volumeState) (bool, error) {
	validatedTarget, err := d.validatedTarget(volumeName, state)
	if err != nil {
		return false, err
	}
	if validatedTarget != target {
		return false, newRecoveryFailure(
			volumeName, target, "target does not match validated confinement", "performed no mount operation",
			fmt.Errorf("validated target is %q", validatedTarget), "retry with the validated persisted definition",
		)
	}
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		return false, newRecoveryFailure(volumeName, target, "unreadable mount table", "made no changes", err, "restore access to /proc/self/mountinfo and retry")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(descendants) > 0 {
		return false, newRecoveryFailure(
			volumeName, target, "unknown nested mount state", "preserved all mounts",
			fmt.Errorf("found %d mount(s) below the managed target", len(descendants)),
			"remove or relocate the nested mounts manually, then retry",
		)
	}
	if len(exact) == 0 {
		return false, nil
	}
	if len(exact) > 1 {
		matching := 0
		for _, record := range exact {
			if recordMatchesExpected(record, state, state.Subdir) {
				matching++
			}
		}
		return false, newRecoveryFailure(
			volumeName, target, "ambiguous duplicate mount stack", "preserved every mount to avoid disrupting active or unknown users",
			fmt.Errorf("found %d mount(s), of which %d match the configured identity", len(exact), matching),
			"inspect the target mount stack and remove conflicts manually, then retry",
		)
	}
	record := exact[0]
	if !recordMatchesExpected(record, state, state.Subdir) {
		return false, newRecoveryFailure(
			volumeName, target, "conflicting or temporary mount identity", "preserved the mount and refused to adopt it",
			fmt.Errorf("found filesystem %q source %q root %q; expected one of %q", record.filesystem, record.source, record.root, expectedMountSources(state, state.Subdir)),
			"verify whether this is a crash-surviving temporary or unknown mount, unmount it manually if safe, then retry",
		)
	}
	if err := d.healthProbe.probe(ctx, target); err != nil {
		if !isStaleMountError(err) {
			return false, newRecoveryFailure(
				volumeName, target, "intended mount with unproven health", "preserved the mount and returned no path",
				err, "restore bounded probe access or unmount the target manually, then retry",
			)
		}
		condition := fmt.Sprintf("disconnected or stale intended GlusterFS mount (%v)", err)
		if err := d.detachExactMount(ctx, volumeName, target, record, condition); err != nil {
			return false, err
		}
		log.Printf("Recovery volume %q target %q: detected %s; recovery result: lazily detached the proven intended mount; next action: retry mounts normally", volumeName, target, condition)
		return false, nil
	}
	return true, nil
}

func (d *glusterfsDriver) detachExactMount(ctx context.Context, volumeName, target string, expected mountRecord, condition string) error {
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		return newRecoveryFailure(volumeName, target, condition, "made no changes because identity revalidation failed", err, "inspect the target mount state manually before retrying")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(descendants) > 0 || len(exact) != 1 || !exact[0].sameIdentity(expected) {
		return newRecoveryFailure(
			volumeName, target, condition, "preserved changed or ambiguous mount state",
			fmt.Errorf("identity revalidation found %d exact and %d nested mount(s)", len(exact), len(descendants)),
			"inspect the target mount stack manually, then retry",
		)
	}
	if err := d.client.unmountLazy(ctx, target); err != nil {
		return newRecoveryFailure(volumeName, target, condition, "failed to detach the proven mount", err, "resolve processes or kernel state holding the target, then retry")
	}
	records, err = d.mountInfo.read(ctx)
	if err != nil {
		return newRecoveryFailure(volumeName, target, condition, "detach completed but verification failed", err, "inspect the target mount state manually before retrying")
	}
	exact, _ = recordsForTarget(records, target)
	if len(exact) != 0 {
		return newRecoveryFailure(volumeName, target, condition, "detach verification found remaining mount state", fmt.Errorf("%d mount(s) remain", len(exact)), "inspect the target mount stack manually, then retry")
	}
	return nil
}

func (d *glusterfsDriver) prepareMountpoint(volumeName, target string, state volumeState) error {
	validatedTarget, err := d.validatedTarget(volumeName, state)
	if err != nil {
		return err
	}
	if validatedTarget != target {
		return newRecoveryFailure(volumeName, target, "target failed confinement revalidation", "made no filesystem change", fmt.Errorf("validated target is %q", validatedTarget), "correct the persisted definition and retry")
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(target, defaultMode); err != nil {
			return newRecoveryFailure(volumeName, target, "missing mountpoint directory", "directory creation failed", err, "fix parent directory permissions or type, then retry")
		}
		return nil
	}
	if err != nil {
		return newRecoveryFailure(volumeName, target, "unreadable mountpoint path", "made no changes", err, "restore access to the target path, then retry")
	}
	if !info.IsDir() {
		return newRecoveryFailure(volumeName, target, "incompatible mountpoint object", "preserved the object", fmt.Errorf("target mode is %s", info.Mode()), "move or remove the incompatible object manually without losing local data, then retry")
	}
	directory, err := os.Open(target)
	if err != nil {
		return newRecoveryFailure(volumeName, target, "unreadable mountpoint directory", "preserved the directory", err, "restore directory access, then retry")
	}
	defer directory.Close()
	names, err := directory.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return newRecoveryFailure(volumeName, target, "unreadable mountpoint directory", "preserved the directory", err, "restore directory access, then retry")
	}
	if len(names) > 0 {
		log.Printf("Volume %q target %q is a non-empty ordinary directory; mounting will hide, but never modify or delete, its local contents", volumeName, target)
	}
	return nil
}

func (d *glusterfsDriver) mountPhysical(ctx context.Context, volumeName, target string, state volumeState) error {
	if state.Subdir != "" {
		temporary, err := d.mountAndVerify(ctx, volumeName, target, state, "", "preparing the GlusterFS subdirectory")
		if err != nil {
			return err
		}
		if d.subdirectories == nil {
			return d.rollbackAttemptMount(ctx, volumeName, target, *temporary, "failed to prepare the GlusterFS subdirectory", errors.New("descriptor-relative subdirectory preparation is not configured"))
		}
		if err := d.subdirectories.prepare(ctx, target, state.Subdir, temporary.record); err != nil {
			return d.rollbackAttemptMount(ctx, volumeName, target, *temporary, "failed to prepare the GlusterFS subdirectory", err)
		}
		if err := d.unmountPhysical(ctx, volumeName, target, state, ""); err != nil {
			return err
		}
	}
	_, err := d.mountAndVerify(ctx, volumeName, target, state, state.Subdir, "mounting the requested GlusterFS path")
	return err
}

func (d *glusterfsDriver) mountAndVerify(ctx context.Context, volumeName, target string, state volumeState, subdir, operation string) (*attemptMount, error) {
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		return nil, newRecoveryFailure(volumeName, target, operation, "did not start the mount command", err, "restore mountinfo access, then retry")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(exact) != 0 || len(descendants) != 0 {
		return nil, newRecoveryFailure(volumeName, target, operation, "did not start the mount command and preserved existing state", fmt.Errorf("found %d exact and %d nested mount(s)", len(exact), len(descendants)), "resolve conflicting mounts, then retry")
	}
	evidence := newMountAttemptEvidence(records, state, subdir)
	if err := d.client.mountWithGlusterfs(ctx, target, state.Volume, state.Servers, subdir); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), rollbackOperationTimeout)
		defer cancel()
		return nil, d.cleanupAfterMountCommand(cleanupCtx, volumeName, target, operation, evidence, err)
	}
	records, err = d.mountInfo.read(ctx)
	if err != nil {
		return nil, newRecoveryFailure(volumeName, target, operation, "preserved unverifiable post-command mount state and added no logical reference", fmt.Errorf("verify mount identity: %w", err), "restore mountinfo access, inspect the target, and retry only after resolving any residual mount")
	}
	exact, descendants = recordsForTarget(records, target)
	if len(descendants) != 0 || len(exact) > 1 {
		return nil, newRecoveryFailure(
			volumeName, target, operation, "preserved an ambiguous post-mount stack rather than disrupting unknown users",
			fmt.Errorf("verification found %d exact and %d nested mount(s)", len(exact), len(descendants)),
			"inspect the target mount stack, remove conflicts manually, then retry",
		)
	}
	if len(exact) == 0 {
		return nil, newRecoveryFailure(volumeName, target, operation, "no physical mount was recorded and no logical reference was added", errors.New("mount command reported success without a mountinfo entry"), "inspect GlusterFS client logs and connectivity, then retry")
	}
	record := exact[0]
	if !recordMatchesExpected(record, state, subdir) {
		return nil, newRecoveryFailure(volumeName, target, operation, "preserved mismatched post-command mount state and added no logical reference", fmt.Errorf("mounted identity source %q does not match expected sources %q", record.source, expectedMountSources(state, subdir)), "inspect and resolve the unknown mount manually, then retry")
	}
	if !evidence.attributes(record) {
		return nil, newRecoveryFailure(volumeName, target, operation, "preserved a mount whose ownership was not attributable to this attempt", fmt.Errorf("mount ID %d was already present before the command", record.mountID), "inspect the target mount identity manually, then retry")
	}
	attempt := &attemptMount{record: record}
	if err := d.healthProbe.probe(ctx, target); err != nil {
		return nil, d.rollbackAttemptMount(ctx, volumeName, target, *attempt, operation, fmt.Errorf("post-mount health verification failed: %w", err))
	}
	return attempt, nil
}

func (d *glusterfsDriver) cleanupAfterMountCommand(ctx context.Context, volumeName, target, operation string, evidence mountAttemptEvidence, mountErr error) error {
	records, inspectErr := d.mountInfo.read(ctx)
	if inspectErr != nil {
		return newRecoveryFailure(volumeName, target, operation, "mount command failed; preserved unverifiable residual state", fmt.Errorf("%w; mountinfo cause: %v", mountErr, inspectErr), "restore mountinfo access and inspect any residual mount manually before retrying")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(descendants) != 0 || len(exact) > 1 {
		return newRecoveryFailure(volumeName, target, operation, "mount command failed; preserved ambiguous residual state", fmt.Errorf("%w; found %d exact and %d nested mount(s)", mountErr, len(exact), len(descendants)), "inspect the target mount stack manually, then retry")
	}
	if len(exact) == 0 {
		return newRecoveryFailure(volumeName, target, operation, "no residual physical mount remains; state is retryable", mountErr, "resolve the GlusterFS cause and retry")
	}
	record := exact[0]
	if !evidence.attributes(record) {
		return newRecoveryFailure(volumeName, target, operation, "mount command failed; preserved residual state not attributable to this attempt", mountErr, "inspect and resolve the residual mount manually, then retry")
	}
	return d.rollbackAttemptMount(ctx, volumeName, target, attemptMount{record: record}, operation, mountErr)
}

func (d *glusterfsDriver) rollbackAttemptMount(ctx context.Context, volumeName, target string, attempt attemptMount, operation string, cause error) error {
	cleanupCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		cleanupCtx, cancel = context.WithTimeout(context.Background(), rollbackOperationTimeout)
	}
	defer cancel()
	if err := d.detachExactMount(cleanupCtx, volumeName, target, attempt.record, operation); err != nil {
		return newRecoveryFailure(volumeName, target, operation, "owned partial mount rollback failed", fmt.Errorf("%w; rollback cause: %v", cause, err), "clear the target mount state manually, then retry")
	}
	return newRecoveryFailure(volumeName, target, operation, "detached the single command-created mount; state is retryable", cause, "resolve the GlusterFS cause and retry")
}

func (d *glusterfsDriver) unmountPhysical(ctx context.Context, volumeName, target string, state volumeState, subdir string) error {
	mounted, err := d.reconcileExpectedTarget(ctx, volumeName, target, state, subdir)
	if err != nil {
		return err
	}
	if !mounted {
		return nil
	}
	if err := d.client.unmount(ctx, target); err != nil {
		return newRecoveryFailure(volumeName, target, "last logical reference release", "physical unmount failed and logical reference was preserved", err, "resolve users holding the mount and retry the unmount")
	}
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		return newRecoveryFailure(volumeName, target, "completed physical unmount", "could not verify the target", err, "inspect the target before retrying")
	}
	exact, _ := recordsForTarget(records, target)
	if len(exact) != 0 {
		return newRecoveryFailure(volumeName, target, "completed physical unmount", "verification found remaining mount state", fmt.Errorf("%d mount(s) remain", len(exact)), "inspect the target mount stack manually, then retry")
	}
	return nil
}

func (d *glusterfsDriver) reconcileExpectedTarget(ctx context.Context, volumeName, target string, state volumeState, subdir string) (bool, error) {
	validatedTarget, err := d.validatedTarget(volumeName, state)
	if err != nil {
		return false, err
	}
	if validatedTarget != target {
		return false, newRecoveryFailure(volumeName, target, "target failed confinement revalidation", "made no mount change", fmt.Errorf("validated target is %q", validatedTarget), "correct the persisted definition and retry")
	}
	if subdir == state.Subdir {
		return d.reconcileTarget(ctx, volumeName, target, state)
	}
	records, err := d.mountInfo.read(ctx)
	if err != nil {
		return false, newRecoveryFailure(volumeName, target, "unreadable mount table", "made no changes", err, "restore mountinfo access and retry")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(descendants) != 0 || len(exact) > 1 {
		return false, newRecoveryFailure(volumeName, target, "ambiguous temporary mount state", "preserved all mounts", fmt.Errorf("found %d exact and %d nested mount(s)", len(exact), len(descendants)), "inspect the target mount stack manually, then retry")
	}
	if len(exact) == 0 {
		return false, nil
	}
	if !recordMatchesExpected(exact[0], state, subdir) {
		return false, newRecoveryFailure(volumeName, target, "temporary mount identity mismatch", "preserved the mount", fmt.Errorf("source %q does not match expected sources %q", exact[0].source, expectedMountSources(state, subdir)), "inspect and resolve the target manually, then retry")
	}
	if err := d.healthProbe.probe(ctx, target); err != nil {
		if isStaleMountError(err) {
			if err := d.detachExactMount(ctx, volumeName, target, exact[0], "stale temporary mount"); err != nil {
				return false, err
			}
			return false, nil
		}
		return false, newRecoveryFailure(volumeName, target, "temporary mount with unproven health", "preserved the mount", err, "restore probe access or inspect the target manually, then retry")
	}
	return true, nil
}
