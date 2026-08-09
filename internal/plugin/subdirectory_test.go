package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDescriptorSubdirectoryPreparerCreatesNestedPathRelativeToVerifiedMount(t *testing.T) {
	target := t.TempDir()
	sentinel := filepath.Join(target, ".local-sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := recordForDirectory(t, target, 201)
	unknown := mountRecord{mountID: 999, target: filepath.Join(filepath.Dir(target), "unknown-mount"), filesystem: "tmpfs", source: "tmpfs"}
	table := &fakeMountInfo{}
	table.set(expected, unknown)
	preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: linuxDirectoryOperations{}}

	if err := preparer.prepare(context.Background(), target, "one/two/three", expected); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(target, "one", "two", "three")); err != nil || !info.IsDir() {
		t.Fatalf("nested path info=%v err=%v", info, err)
	}
	assertFileContents(t, sentinel, "untouched")
	records := table.snapshot()
	if len(records) != 2 || !records[1].sameIdentity(unknown) {
		t.Fatalf("unknown mount fixture was changed: %#v", records)
	}
}

func TestDescriptorSubdirectoryPreparerPreservesStateWhenTemporaryMountDisappearsOrChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		second []mountRecord
	}{
		{name: "disappears", second: nil},
		{name: "is replaced", second: []mountRecord{{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			sentinel := filepath.Join(target, ".local-sentinel")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			expected := recordForDirectory(t, target, 202)
			second := test.second
			if second != nil {
				replacement := expected
				replacement.mountID++
				replacement.source = "unknown-server:replacement"
				second = []mountRecord{replacement}
			}
			table := &scriptedMountInfo{results: []mountInfoResult{
				{records: []mountRecord{expected}},
				{records: second},
			}}
			preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: linuxDirectoryOperations{}}

			err := preparer.prepare(context.Background(), target, "must/not/appear", expected)
			if err == nil || !strings.Contains(err.Error(), "temporary mount changed before subdirectory mutation") {
				t.Fatalf("identity change error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(target, "must")); !os.IsNotExist(err) {
				t.Fatalf("subdirectory mutation occurred after identity change: %v", err)
			}
			assertFileContents(t, sentinel, "untouched")
		})
	}
}

func TestDescriptorSubdirectoryPreparerRejectsSymlinkComponentWithoutTouchingDestination(t *testing.T) {
	target := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "linked")); err != nil {
		t.Fatal(err)
	}
	expected := recordForDirectory(t, target, 203)
	table := &fakeMountInfo{}
	table.set(expected)
	preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: linuxDirectoryOperations{}}

	err := preparer.prepare(context.Background(), target, "linked/child", expected)
	if err == nil || !strings.Contains(err.Error(), `open subdirectory component "linked" without following symlinks`) {
		t.Fatalf("symlink error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "child")); !os.IsNotExist(err) {
		t.Fatalf("symlink destination was modified: %v", err)
	}
	assertFileContents(t, sentinel, "outside")
}

func TestDescriptorSubdirectoryPreparerRejectsMountDeviceMismatchBeforeMutation(t *testing.T) {
	target := t.TempDir()
	expected := recordForDirectory(t, target, 204)
	expected.majorMinor = "999:999"
	table := &fakeMountInfo{}
	table.set(expected)
	preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: linuxDirectoryOperations{}}

	err := preparer.prepare(context.Background(), target, "must/not/appear", expected)
	if err == nil || !strings.Contains(err.Error(), "does not match verified mount device") {
		t.Fatalf("device mismatch error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "must")); !os.IsNotExist(err) {
		t.Fatalf("device mismatch mutated target: %v", err)
	}
}

func TestDescriptorSubdirectoryPreparerPropagatesDescriptorSyscallFailuresWithoutUnsafeMutation(t *testing.T) {
	tests := []struct {
		name     string
		fault    func(*faultDirectoryOperations)
		wantText string
	}{
		{name: "open", fault: func(ops *faultDirectoryOperations) { ops.openErr = unix.EACCES }, wantText: "open verified temporary mount"},
		{name: "root fstat", fault: func(ops *faultDirectoryOperations) { ops.fstatErr = unix.EIO }, wantText: "inspect temporary mount handle"},
		{name: "openat", fault: func(ops *faultDirectoryOperations) { ops.openatErr = unix.EIO }, wantText: `open subdirectory component "new"`},
		{name: "mkdirat", fault: func(ops *faultDirectoryOperations) { ops.mkdiratErr = unix.EROFS }, wantText: `create subdirectory component "new"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := t.TempDir()
			sentinel := filepath.Join(target, ".local-sentinel")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			expected := recordForDirectory(t, target, 205)
			table := &fakeMountInfo{}
			table.set(expected)
			operations := &faultDirectoryOperations{base: linuxDirectoryOperations{}}
			test.fault(operations)
			preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: operations}

			err := preparer.prepare(context.Background(), target, "new/nested", expected)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("syscall failure error = %v, want %q", err, test.wantText)
			}
			if _, err := os.Lstat(filepath.Join(target, "new")); !os.IsNotExist(err) {
				t.Fatalf("failed syscall left target mutation: %v", err)
			}
			assertFileContents(t, sentinel, "untouched")
		})
	}
}

func recordForDirectory(t *testing.T, target string, mountID int) mountRecord {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Stat(target, &stat); err != nil {
		t.Fatal(err)
	}
	return mountRecord{
		mountID:      mountID,
		parentID:     1,
		majorMinor:   linuxDevice(uint64(stat.Dev)),
		root:         "/",
		target:       target,
		mountOptions: "rw,nosuid",
		filesystem:   "fuse.glusterfs",
		source:       "server-a:gv0",
		superOptions: "rw,relatime",
	}
}

type faultDirectoryOperations struct {
	base       directoryOperations
	openErr    error
	openatErr  error
	mkdiratErr error
	fstatErr   error
}

func (o *faultDirectoryOperations) open(path string, flags int, mode uint32) (int, error) {
	if o.openErr != nil {
		return -1, o.openErr
	}
	return o.base.open(path, flags, mode)
}

func (o *faultDirectoryOperations) openat(directory int, path string, flags int, mode uint32) (int, error) {
	if o.openatErr != nil {
		return -1, o.openatErr
	}
	return o.base.openat(directory, path, flags, mode)
}

func (o *faultDirectoryOperations) mkdirat(directory int, path string, mode uint32) error {
	if o.mkdiratErr != nil {
		return o.mkdiratErr
	}
	return o.base.mkdirat(directory, path, mode)
}

func (o *faultDirectoryOperations) fstat(file int, stat *unix.Stat_t) error {
	if o.fstatErr != nil {
		return o.fstatErr
	}
	return o.base.fstat(file, stat)
}

func (o *faultDirectoryOperations) close(file int) error {
	return o.base.close(file)
}

var _ directoryOperations = (*faultDirectoryOperations)(nil)

func TestDescriptorSubdirectoryPreparerRejectsCancelledContextBeforeComponents(t *testing.T) {
	target := t.TempDir()
	expected := recordForDirectory(t, target, 206)
	table := &fakeMountInfo{}
	table.set(expected)
	preparer := descriptorSubdirectoryPreparer{mountInfo: table, operations: linuxDirectoryOperations{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := preparer.prepare(ctx, target, "not/created", expected)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation error = %v", err)
	}
}
