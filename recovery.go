package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const mountInfoPath = "/proc/self/mountinfo"

type mountRecord struct {
	target     string
	filesystem string
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

func readMountInfo() ([]mountRecord, error) {
	file, err := os.Open(mountInfoPath)
	if err != nil {
		return nil, fmt.Errorf("open mount information: %w", err)
	}
	defer file.Close()

	var records []mountRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " - ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid mountinfo entry %q", scanner.Text())
		}
		left := strings.Fields(parts[0])
		right := strings.Fields(parts[1])
		if len(left) < 5 || len(right) < 2 {
			return nil, fmt.Errorf("incomplete mountinfo entry %q", scanner.Text())
		}
		target, err := unescapeMountInfoField(left[4])
		if err != nil {
			return nil, fmt.Errorf("decode mount target %q: %w", left[4], err)
		}
		records = append(records, mountRecord{
			target:     target,
			filesystem: right[0],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read mount information: %w", err)
	}
	return records, nil
}

func unescapeMountInfoField(value string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) {
			return "", fmt.Errorf("truncated escape")
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

func isManagedGlusterMount(record mountRecord) bool {
	return record.filesystem == "fuse.glusterfs"
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
	d.Lock()
	defer d.Unlock()

	for name := range d.volumes {
		target := d.mountpoint(name)
		mounted, err := d.reconcileTarget(name, target)
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
			log.Printf("Startup recovery volume %q target %q: detected one usable managed mount; recovery result: preserved for request reuse", name, target)
			continue
		}
		if err := d.prepareMountpoint(name, target); err != nil {
			d.recoveryIssues[name] = err
			log.Printf("Startup recovery blocked: %v", err)
			continue
		}
		log.Printf("Startup recovery volume %q target %q: recovery result: ready without a physical mount", name, target)
	}

	d.warnUnknownMounts()
}

func (d *glusterfsDriver) warnUnknownMounts() {
	records, err := readMountInfo()
	if err != nil {
		log.Printf("Startup recovery could not scan for unknown mounts under %q: %v; next action: inspect mountinfo before using a conflicting target", d.root, err)
		return
	}
	known := make(map[string]struct{}, len(d.volumes))
	for name := range d.volumes {
		known[filepath.Clean(d.mountpoint(name))] = struct{}{}
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

func (d *glusterfsDriver) reconcileTarget(volumeName, target string) (bool, error) {
	records, err := readMountInfo()
	if err != nil {
		return false, newRecoveryFailure(volumeName, target, "unreadable mount table", "no changes made", err, "restore access to /proc/self/mountinfo and retry")
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
	for _, record := range exact {
		if !isManagedGlusterMount(record) {
			return false, newRecoveryFailure(
				volumeName, target, "conflicting unknown mount", "preserved the mount",
				fmt.Errorf("filesystem %q is not a managed GlusterFS mount", record.filesystem),
				"unmount or relocate the conflicting mount manually, then retry",
			)
		}
	}
	if len(exact) > 1 {
		condition := fmt.Sprintf("%d duplicate managed GlusterFS mounts", len(exact))
		if err := d.detachManagedMounts(volumeName, target, len(exact), condition); err != nil {
			return false, err
		}
		log.Printf("Recovery volume %q target %q: detected %s; recovery result: lazily detached all duplicates; next action: retry mounts normally", volumeName, target, condition)
		return false, nil
	}
	if err := probeMountedDirectory(target); err != nil {
		if !isStaleMountError(err) {
			return false, newRecoveryFailure(
				volumeName, target, "managed mount with indeterminate health", "preserved the mount",
				err, "restore access to the mounted directory or unmount it manually, then retry",
			)
		}
		condition := fmt.Sprintf("disconnected or stale managed GlusterFS mount (%v)", err)
		if err := d.detachManagedMounts(volumeName, target, 1, condition); err != nil {
			return false, err
		}
		log.Printf("Recovery volume %q target %q: detected %s; recovery result: lazily detached the stale mount; next action: retry mounts normally", volumeName, target, condition)
		return false, nil
	}
	return true, nil
}

func (d *glusterfsDriver) detachManagedMounts(volumeName, target string, count int, condition string) error {
	for detached := 0; detached < count; detached++ {
		if err := d.client.unmountLazy(target); err != nil {
			return newRecoveryFailure(
				volumeName, target, condition,
				fmt.Sprintf("detached %d of %d managed mount(s)", detached, count),
				err, "resolve processes or kernel state holding the target, then retry",
			)
		}
	}
	records, err := readMountInfo()
	if err != nil {
		return newRecoveryFailure(volumeName, target, condition, "detach commands completed but verification failed", err, "inspect the target mount state manually before retrying")
	}
	exact, _ := recordsForTarget(records, target)
	if len(exact) != 0 {
		return newRecoveryFailure(
			volumeName, target, condition, "detach verification found remaining mount state",
			fmt.Errorf("%d mount(s) remain at the target", len(exact)),
			"inspect and resolve the target mount stack manually, then retry",
		)
	}
	return nil
}

func (d *glusterfsDriver) prepareMountpoint(volumeName, target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(target, defaultMode); err != nil {
			return newRecoveryFailure(volumeName, target, "missing mountpoint directory", "directory creation failed", err, "fix parent directory permissions or type, then retry")
		}
		return nil
	}
	if err != nil {
		return newRecoveryFailure(volumeName, target, "unreadable mountpoint path", "no changes made", err, "restore access to the target path, then retry")
	}
	if !info.IsDir() {
		return newRecoveryFailure(
			volumeName, target, "incompatible mountpoint object", "preserved the object",
			fmt.Errorf("target mode is %s", info.Mode()),
			"move or remove the incompatible object manually without losing local data, then retry",
		)
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

func (d *glusterfsDriver) mountPhysical(volumeName, target string, state volumeState) error {
	if state.Subdir != "" {
		if err := d.mountAndVerify(volumeName, target, state.Volume, state.Servers, "", "preparing the GlusterFS subdirectory"); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(target, state.Subdir), defaultMode); err != nil {
			return d.failedMountCleanup(volumeName, target, "failed to prepare the GlusterFS subdirectory", err)
		}
		if err := d.unmountPhysical(volumeName, target); err != nil {
			return err
		}
	}
	return d.mountAndVerify(volumeName, target, state.Volume, state.Servers, state.Subdir, "mounting the requested GlusterFS path")
}

func (d *glusterfsDriver) mountAndVerify(volumeName, target, volume string, servers []string, subdir, operation string) error {
	if err := d.client.mountWithGlusterfs(target, volume, servers, subdir); err != nil {
		return d.failedMountCleanup(volumeName, target, operation, err)
	}
	mounted, err := d.reconcileTarget(volumeName, target)
	if err != nil {
		return err
	}
	if !mounted {
		return newRecoveryFailure(
			volumeName, target, "mount command reported success without one usable physical mount",
			"removed any duplicate or stale managed mount state", errors.New("post-mount verification failed"),
			"inspect GlusterFS client logs and connectivity, then retry",
		)
	}
	return nil
}

func (d *glusterfsDriver) failedMountCleanup(volumeName, target, condition string, mountErr error) error {
	records, inspectErr := readMountInfo()
	if inspectErr != nil {
		cause := fmt.Errorf("%w; residual mount inspection also failed: %v", mountErr, inspectErr)
		return newRecoveryFailure(volumeName, target, condition, "could not inspect residual mount state", cause, "inspect and clear the target manually before retrying")
	}
	exact, descendants := recordsForTarget(records, target)
	if len(descendants) > 0 {
		return newRecoveryFailure(volumeName, target, condition, "preserved unknown nested mounts", mountErr, "resolve the mount failure and nested mounts manually, then retry")
	}
	if len(exact) == 0 {
		return newRecoveryFailure(volumeName, target, condition, "no residual physical mount remains; state is retryable", mountErr, "resolve the GlusterFS cause and retry")
	}
	for _, record := range exact {
		if !isManagedGlusterMount(record) {
			return newRecoveryFailure(volumeName, target, condition, "preserved a conflicting unknown mount", mountErr, "resolve the GlusterFS failure and conflicting mount manually, then retry")
		}
	}
	if err := d.detachManagedMounts(volumeName, target, len(exact), condition); err != nil {
		cause := fmt.Errorf("%w; residual mount cleanup also failed: %v", mountErr, err)
		return newRecoveryFailure(volumeName, target, condition, "residual managed mount cleanup failed", cause, "clear the target mount state manually, then retry")
	}
	return newRecoveryFailure(volumeName, target, condition, "detached residual managed mount state; state is retryable", mountErr, "resolve the GlusterFS cause and retry")
}

func (d *glusterfsDriver) unmountPhysical(volumeName, target string) error {
	mounted, err := d.reconcileTarget(volumeName, target)
	if err != nil {
		return err
	}
	if !mounted {
		return nil
	}
	if err := d.client.unmount(target); err != nil {
		return newRecoveryFailure(volumeName, target, "last logical reference release", "physical unmount failed and logical reference was preserved", err, "resolve users holding the mount and retry the unmount")
	}
	records, err := readMountInfo()
	if err != nil {
		return newRecoveryFailure(volumeName, target, "completed physical unmount", "could not verify the target", err, "inspect the target before retrying")
	}
	exact, _ := recordsForTarget(records, target)
	if len(exact) != 0 {
		return newRecoveryFailure(volumeName, target, "completed physical unmount", "verification found remaining mount state", fmt.Errorf("%d mount(s) remain", len(exact)), "inspect the target mount stack manually, then retry")
	}
	return nil
}
