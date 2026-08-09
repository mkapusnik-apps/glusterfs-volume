package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

type subdirectoryPreparer interface {
	prepare(context.Context, string, string, mountRecord) error
}

type descriptorSubdirectoryPreparer struct {
	mountInfo  mountInfoReader
	operations directoryOperations
}

type directoryOperations interface {
	open(string, int, uint32) (int, error)
	openat(int, string, int, uint32) (int, error)
	mkdirat(int, string, uint32) error
	fstat(int, *unix.Stat_t) error
	close(int) error
}

type linuxDirectoryOperations struct{}

func (linuxDirectoryOperations) open(path string, flags int, mode uint32) (int, error) {
	return unix.Open(path, flags, mode)
}

func (linuxDirectoryOperations) openat(directory int, path string, flags int, mode uint32) (int, error) {
	return unix.Openat(directory, path, flags, mode)
}

func (linuxDirectoryOperations) mkdirat(directory int, path string, mode uint32) error {
	return unix.Mkdirat(directory, path, mode)
}

func (linuxDirectoryOperations) fstat(file int, stat *unix.Stat_t) error {
	return unix.Fstat(file, stat)
}

func (linuxDirectoryOperations) close(file int) error {
	return unix.Close(file)
}

func (p descriptorSubdirectoryPreparer) prepare(ctx context.Context, target, subdir string, expected mountRecord) error {
	if p.mountInfo == nil || p.operations == nil {
		return errors.New("descriptor-relative subdirectory preparation is not configured")
	}
	if err := validateSubdirectory(subdir); err != nil {
		return err
	}
	if err := p.verifyIdentity(ctx, target, expected); err != nil {
		return fmt.Errorf("verify temporary mount before opening: %w", err)
	}

	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	root, err := p.operations.open(target, flags, 0)
	if err != nil {
		return fmt.Errorf("open verified temporary mount without following symlinks: %w", err)
	}
	defer p.operations.close(root)

	var rootStat unix.Stat_t
	if err := p.operations.fstat(root, &rootStat); err != nil {
		return fmt.Errorf("inspect temporary mount handle: %w", err)
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("temporary mount handle is not a directory")
	}
	if actual := linuxDevice(uint64(rootStat.Dev)); actual != expected.majorMinor {
		return fmt.Errorf("temporary mount handle device %q does not match verified mount device %q", actual, expected.majorMinor)
	}
	if err := p.verifyIdentity(ctx, target, expected); err != nil {
		return fmt.Errorf("temporary mount changed before subdirectory mutation: %w", err)
	}

	return p.mkdirAllAt(ctx, root, uint64(rootStat.Dev), subdir, flags)
}

func (p descriptorSubdirectoryPreparer) verifyIdentity(ctx context.Context, target string, expected mountRecord) error {
	records, err := p.mountInfo.read(ctx)
	if err != nil {
		return fmt.Errorf("read mount identity: %w", err)
	}
	exact, descendants := recordsForTarget(records, target)
	if len(exact) != 1 || len(descendants) != 0 || !exact[0].sameIdentity(expected) {
		return fmt.Errorf("expected one unchanged mount and no nested mounts; found %d exact and %d nested mount(s)", len(exact), len(descendants))
	}
	return nil
}

func (p descriptorSubdirectoryPreparer) mkdirAllAt(ctx context.Context, root int, rootDevice uint64, subdir string, flags int) error {
	current := root
	ownedCurrent := false
	defer func() {
		if ownedCurrent {
			_ = p.operations.close(current)
		}
	}()

	for _, component := range strings.Split(subdir, "/") {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := p.operations.openat(current, component, flags, 0)
		if errors.Is(err, unix.ENOENT) {
			if err := p.operations.mkdirat(current, component, uint32(defaultMode)); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("create subdirectory component %q: %w", component, err)
			}
			next, err = p.operations.openat(current, component, flags, 0)
		}
		if err != nil {
			return fmt.Errorf("open subdirectory component %q without following symlinks: %w", component, err)
		}

		var stat unix.Stat_t
		if err := p.operations.fstat(next, &stat); err != nil {
			_ = p.operations.close(next)
			return fmt.Errorf("inspect subdirectory component %q: %w", component, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(stat.Dev) != rootDevice {
			_ = p.operations.close(next)
			return fmt.Errorf("subdirectory component %q is not a directory on the verified mount device", component)
		}
		if ownedCurrent {
			_ = p.operations.close(current)
		}
		current = next
		ownedCurrent = true
	}
	return nil
}

func linuxDevice(device uint64) string {
	major := ((device >> 8) & 0xfff) | ((device >> 32) & 0xfffff000)
	minor := (device & 0xff) | ((device >> 12) & 0xffffff00)
	return fmt.Sprintf("%d:%d", major, minor)
}
