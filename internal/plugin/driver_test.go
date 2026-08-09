package plugin

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/go-plugins-helpers/volume"
)

func TestMountAndUnmountCountRepeatedAndDistinctClientReferences(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	installSuccessfulMountBehavior(table, client, state)
	target := filepath.Join(driver.root, "volume-a")

	requests := []*volume.MountRequest{
		{Name: "volume-a", ID: "repeated"},
		{Name: "volume-a", ID: "repeated"},
		{Name: "volume-a", ID: "distinct"},
	}
	for _, request := range requests {
		response, err := driver.Mount(request)
		if err != nil {
			t.Fatalf("Mount(%q): %v", request.ID, err)
		}
		if response.Mountpoint != target {
			t.Fatalf("mountpoint = %q, want %q", response.Mountpoint, target)
		}
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 3, map[string]int{"repeated": 2, "distinct": 1})
	if mounts, _, _ := client.counts(); mounts != 1 {
		t.Fatalf("physical mounts = %d, want one shared mount", mounts)
	}

	if err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "unknown"}); err == nil {
		t.Fatal("unknown unmount unexpectedly succeeded")
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 3, map[string]int{"repeated": 2, "distinct": 1})

	if err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "repeated"}); err != nil {
		t.Fatal(err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 2, map[string]int{"repeated": 1, "distinct": 1})
	if err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "repeated"}); err != nil {
		t.Fatal(err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"distinct": 1})

	if err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "repeated"}); err == nil {
		t.Fatal("excess repeated-ID unmount unexpectedly succeeded")
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"distinct": 1})

	if err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "distinct"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatalf("last balanced unmount left logical state: %#v", driver.mounts["volume-a"])
	}
	if len(table.snapshot()) != 0 {
		t.Fatalf("last balanced unmount left physical state: %#v", table.snapshot())
	}
	_, unmounts, lazy := client.counts()
	if unmounts != 1 || lazy != 0 {
		t.Fatalf("physical unmounts=%d lazy=%d, want 1/0", unmounts, lazy)
	}
}

func TestSubdirectoryMountUsesTemporaryRootThenVerifiedSubdirectory(t *testing.T) {
	state := testState("volume-a", "configs/speed")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	installSuccessfulMountBehavior(table, client, state)
	preparer := &fakeSubdirectoryPreparer{}
	driver.subdirectories = preparer
	target := filepath.Join(driver.root, "volume-a")

	response, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Mountpoint != target {
		t.Fatalf("mountpoint = %q", response.Mountpoint)
	}
	if got, want := client.mountedSubdirs(), []string{"", "configs/speed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mount subdirectories = %#v, want %#v", got, want)
	}
	_, unmounts, lazy := client.counts()
	if unmounts != 1 || lazy != 0 {
		t.Fatalf("temporary-root unmounts=%d lazy=%d", unmounts, lazy)
	}
	records := table.snapshot()
	if len(records) != 1 || !recordMatchesExpected(records[0], state, state.Subdir) {
		t.Fatalf("final mount identity = %#v", records)
	}
	preparations := preparer.snapshot()
	if len(preparations) != 1 {
		t.Fatalf("subdirectory preparations = %#v", preparations)
	}
	if preparations[0].target != target || preparations[0].subdir != state.Subdir || !recordMatchesExpected(preparations[0].expected, state, "") {
		t.Fatalf("unsafe or incorrect subdirectory preparation = %#v", preparations[0])
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
}

func TestPostMountIdentityMismatchIsPreservedWithoutPhantomAndRetriesOnlyAfterResolution(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	call := 0
	client.mountFn = func(_ context.Context, target, _ string, _ []string, subdir string) error {
		call++
		record := intendedRecord(target, state, subdir, 30+call)
		if call == 1 {
			record.source = "server-a:wrong-volume"
		}
		table.set(record)
		return nil
	}

	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err == nil || !strings.Contains(err.Error(), "preserved mismatched post-command mount state and added no logical reference") || !strings.Contains(err.Error(), "inspect and resolve the unknown mount manually") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatalf("failed mount left phantom references: %#v", driver.mounts["volume-a"])
	}
	if len(table.snapshot()) != 1 || table.snapshot()[0].source != "server-a:wrong-volume" {
		t.Fatalf("mismatched unknown mount was not preserved: %#v", table.snapshot())
	}
	_, _, lazy := client.counts()
	if lazy != 0 {
		t.Fatalf("mismatched unknown mount was lazily unmounted %d time(s)", lazy)
	}

	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err == nil || !strings.Contains(err.Error(), "conflicting or temporary mount identity") {
		t.Fatalf("retry before conflict resolution error = %v", err)
	}
	if mounts, _, _ := client.counts(); mounts != 1 {
		t.Fatalf("retry before resolution issued another mount command; calls=%d", mounts)
	}

	table.set() // Simulate explicit operator conflict resolution.
	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err != nil {
		t.Fatalf("retry after conflict resolution failed: %v", err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
	if driver.recoveryIssues["volume-a"] != nil {
		t.Fatalf("successful retry retained issue: %v", driver.recoveryIssues["volume-a"])
	}
}

func TestMountCommandSuccessWithoutMountInfoNeverAddsReferenceAndCanRetry(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "no physical mount was recorded and no logical reference was added") {
		t.Fatalf("false-success mount error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("mount command success without mountinfo added a phantom reference")
	}
	if mounts, unmounts, lazy := client.counts(); mounts != 1 || unmounts != 0 || lazy != 0 {
		t.Fatalf("connector calls after false success = mount:%d unmount:%d lazy:%d", mounts, unmounts, lazy)
	}

	installSuccessfulMountBehavior(table, client, state)
	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err != nil {
		t.Fatalf("retry after false success failed: %v", err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
}

func TestPostMountUnhealthyProbeRollsBackOwnedMount(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
	installSuccessfulMountBehavior(table, client, state)
	probe.err = errors.New("probe timed out")

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "detached the single command-created mount; state is retryable") || !strings.Contains(err.Error(), "post-mount health verification failed") {
		t.Fatalf("unhealthy post-mount error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("unhealthy post-mount probe added a phantom reference")
	}
	if len(table.snapshot()) != 0 {
		t.Fatalf("unhealthy owned mount was not rolled back: %#v", table.snapshot())
	}
	_, _, lazy := client.counts()
	if lazy != 1 {
		t.Fatalf("unhealthy rollback lazy unmounts = %d", lazy)
	}
}

func TestPostMountVerificationFailurePreservesUnverifiableStateAndAddsNoReference(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	table.errors = map[int]error{3: errors.New("mountinfo temporarily unavailable")}
	client.mountFn = func(_ context.Context, target, _ string, _ []string, subdir string) error {
		record := intendedRecord(target, state, subdir, 40)
		record.source = "unknown-server:unverifiable-volume"
		table.set(record)
		return nil
	}

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "preserved unverifiable post-command mount state and added no logical reference") || !strings.Contains(err.Error(), "inspect the target") {
		t.Fatalf("verification failure error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("unverified mount added a logical reference")
	}
	if len(table.snapshot()) != 1 || table.snapshot()[0].source != "unknown-server:unverifiable-volume" {
		t.Fatalf("unverifiable post-command record was not preserved: %#v", table.snapshot())
	}
	_, _, lazy := client.counts()
	if lazy != 0 {
		t.Fatalf("unverifiable record was lazily unmounted %d time(s)", lazy)
	}
}

func TestMountCommandFailurePreservesSoleUnexpectedPostSnapshotRecord(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	client.mountFn = func(_ context.Context, target, _ string, _ []string, _ string) error {
		record := intendedRecord(target, state, "", 45)
		record.source = "unknown-server:unknown-volume"
		table.set(record)
		return errors.New("mount command failed while unknown mount appeared")
	}

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "preserved residual state not attributable to this attempt") || !strings.Contains(err.Error(), "inspect and resolve the residual mount manually") {
		t.Fatalf("unexpected residual error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("unexpected residual mount added a logical reference")
	}
	if records := table.snapshot(); len(records) != 1 || records[0].source != "unknown-server:unknown-volume" {
		t.Fatalf("unexpected record was not preserved: %#v", records)
	}
	_, unmounts, lazy := client.counts()
	if unmounts != 0 || lazy != 0 {
		t.Fatalf("unexpected record was detached: unmount=%d lazy=%d", unmounts, lazy)
	}
}

func TestCleanupFailurePreservesTruthfulResidualStateAndCanBeRetried(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	mountFailure := errors.New("simulated gluster command failure")
	cleanupFailure := errors.New("simulated lazy unmount failure")
	client.mountFn = func(_ context.Context, target, _ string, _ []string, subdir string) error {
		table.set(intendedRecord(target, state, subdir, 50))
		return mountFailure
	}
	client.lazyFn = func(_ context.Context, _ string) error { return cleanupFailure }

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "owned partial mount rollback failed") || !strings.Contains(err.Error(), mountFailure.Error()) || !strings.Contains(err.Error(), cleanupFailure.Error()) {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("cleanup failure added a phantom reference")
	}
	if len(table.snapshot()) != 1 {
		t.Fatalf("truthful residual state not represented by fake mount table: %#v", table.snapshot())
	}
	if driver.recoveryIssues["volume-a"] == nil {
		t.Fatal("cleanup failure was not exposed in recovery status")
	}

	// The residual mount has the exact intended identity and remains healthy, so
	// a retry can safely adopt it without issuing a duplicate physical mount.
	client.lazyFn = func(_ context.Context, _ string) error {
		table.set()
		return nil
	}
	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err != nil {
		t.Fatalf("retry did not safely adopt intended residual mount: %v", err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
	if mounts, _, _ := client.counts(); mounts != 1 {
		t.Fatalf("retry created a duplicate physical mount; mount calls=%d", mounts)
	}
}

func TestAmbiguousPostMountStackNeverSucceedsOrRollsBackUnknownUsers(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	client.mountFn = func(_ context.Context, target, _ string, _ []string, subdir string) error {
		table.set(
			intendedRecord(target, state, subdir, 60),
			intendedRecord(target, state, subdir, 61),
		)
		return nil
	}

	_, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "preserved an ambiguous post-mount stack") {
		t.Fatalf("ambiguous post-mount error = %v", err)
	}
	if _, ok := driver.mounts["volume-a"]; ok {
		t.Fatal("ambiguous stack added a reference")
	}
	if len(table.snapshot()) != 2 {
		t.Fatal("ambiguous stack was modified")
	}
	_, unmounts, lazy := client.counts()
	if unmounts != 0 || lazy != 0 {
		t.Fatalf("ambiguous stack triggered unmount=%d lazy=%d", unmounts, lazy)
	}
}

func TestLastUnmountFailurePreservesFinalReferenceForRetry(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, client, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	installSuccessfulMountBehavior(table, client, state)
	if _, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"}); err != nil {
		t.Fatal(err)
	}
	client.unmountFn = func(_ context.Context, _ string) error { return errors.New("target busy") }

	err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "client-a"})
	if err == nil || !strings.Contains(err.Error(), "physical unmount failed and logical reference was preserved") {
		t.Fatalf("last unmount error = %v", err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
	if len(table.snapshot()) != 1 {
		t.Fatal("failed physical unmount lost mount state")
	}
}

func TestPathMissingPhysicalMountReturnsNoFalseSuccessAndPreservesReferences(t *testing.T) {
	state := testState("volume-a", "")
	driver, table, _, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
	target := filepath.Join(driver.root, "volume-a")
	driver.mounts["volume-a"] = &activeMount{connections: 2, mountpoint: target, ids: map[string]int{"client-a": 2}}
	table.set()

	response, err := driver.Path(&volume.PathRequest{Name: "volume-a"})
	if err == nil || response.Mountpoint != "" || !strings.Contains(err.Error(), "missing physical mount with active logical references") {
		t.Fatalf("Path response=%#v error=%v", response, err)
	}
	assertActiveReferences(t, driver.mounts["volume-a"], 2, map[string]int{"client-a": 2})
	if driver.recoveryIssues["volume-a"] == nil {
		t.Fatal("missing physical mount was not exposed in status")
	}
}

func assertActiveReferences(t *testing.T, mount *activeMount, connections int, ids map[string]int) {
	t.Helper()
	if mount == nil {
		t.Fatal("active mount is nil")
	}
	if mount.connections != connections {
		t.Fatalf("connections = %d, want %d", mount.connections, connections)
	}
	if !reflect.DeepEqual(mount.ids, ids) {
		t.Fatalf("IDs = %#v, want %#v", mount.ids, ids)
	}
}
