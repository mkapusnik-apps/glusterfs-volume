package plugin

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/go-plugins-helpers/volume"
)

func TestFinalReferenceUnmountFailsClosedWhenMountChangesAfterHealthProbe(t *testing.T) {
	for _, test := range regularUnmountRevalidationCases() {
		t.Run(test.name, func(t *testing.T) {
			state := testState("volume-a", "")
			driver, _, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
			target := filepath.Join(driver.root, "volume-a")
			accepted := intendedRecord(target, state, "", 301)
			driver.mountInfo = &scriptedMountInfo{results: []mountInfoResult{
				{records: []mountRecord{accepted}},
				test.result(target, state, accepted),
			}}
			driver.mounts["volume-a"] = &activeMount{
				connections: 1,
				mountpoint:  target,
				ids:         map[string]int{"client-a": 1},
			}

			err := driver.Unmount(&volume.UnmountRequest{Name: "volume-a", ID: "client-a"})
			assertActionableRegularUnmountRevalidationError(t, err)
			assertConnectorDidNotUnmount(t, client)
			assertActiveReferences(t, driver.mounts["volume-a"], 1, map[string]int{"client-a": 1})
			if driver.recoveryIssues["volume-a"] == nil || driver.recoveryIssues["volume-a"].Error() != err.Error() {
				t.Fatalf("recovery issue = %v, want unmount error %v", driver.recoveryIssues["volume-a"], err)
			}
			if probe.count() != 1 {
				t.Fatalf("health probes = %d, want candidate probed before revalidation", probe.count())
			}
		})
	}
}

func TestRemoveFailsClosedWhenMountChangesAfterHealthProbe(t *testing.T) {
	for _, test := range regularUnmountRevalidationCases() {
		t.Run(test.name, func(t *testing.T) {
			state := testState("volume-a", "")
			driver, _, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
			target := filepath.Join(driver.root, "volume-a")
			accepted := intendedRecord(target, state, "", 311)
			driver.mountInfo = &scriptedMountInfo{results: []mountInfoResult{
				{records: []mountRecord{accepted}},
				test.result(target, state, accepted),
			}}

			err := driver.Remove(&volume.RemoveRequest{Name: "volume-a"})
			assertActionableRegularUnmountRevalidationError(t, err)
			assertConnectorDidNotUnmount(t, client)
			if got, ok := driver.volumes["volume-a"]; !ok || !reflect.DeepEqual(got, state) {
				t.Fatalf("failed remove lost or changed volume definition: %#v", driver.volumes)
			}
			if driver.recoveryIssues["volume-a"] == nil || driver.recoveryIssues["volume-a"].Error() != err.Error() {
				t.Fatalf("recovery issue = %v, want remove error %v", driver.recoveryIssues["volume-a"], err)
			}
			if probe.count() != 1 {
				t.Fatalf("health probes = %d, want candidate probed before revalidation", probe.count())
			}
		})
	}
}

func TestTemporaryRootCleanupFailsClosedWhenMountChangesAfterHealthProbe(t *testing.T) {
	for _, test := range regularUnmountRevalidationCases() {
		t.Run(test.name, func(t *testing.T) {
			state := testState("volume-a", "configs/speed")
			driver, _, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
			target := filepath.Join(driver.root, "volume-a")
			temporary := intendedRecord(target, state, "", 321)
			driver.mountInfo = &scriptedMountInfo{results: []mountInfoResult{
				{},                                  // Initial requested-subdirectory reconciliation.
				{},                                  // Temporary-root pre-command snapshot.
				{records: []mountRecord{temporary}}, // Temporary-root post-command verification.
				{records: []mountRecord{temporary}}, // Candidate selection and health probe before regular unmount.
				test.result(target, state, temporary),
			}}
			preparer := &fakeSubdirectoryPreparer{}
			driver.subdirectories = preparer

			response, err := driver.Mount(&volume.MountRequest{Name: "volume-a", ID: "client-a"})
			if response.Mountpoint != "" {
				t.Fatalf("failed temporary cleanup returned mountpoint %q", response.Mountpoint)
			}
			assertActionableRegularUnmountRevalidationError(t, err)
			mounts, unmounts, lazy := client.counts()
			if mounts != 1 || unmounts != 0 || lazy != 0 {
				t.Fatalf("connector calls = mount:%d unmount:%d lazy:%d, want 1/0/0", mounts, unmounts, lazy)
			}
			if got := client.mountedSubdirs(); !reflect.DeepEqual(got, []string{""}) {
				t.Fatalf("mount commands = %#v, final subdirectory mount must not start", got)
			}
			if _, ok := driver.mounts["volume-a"]; ok {
				t.Fatalf("failed temporary cleanup added logical state: %#v", driver.mounts["volume-a"])
			}
			if got, ok := driver.volumes["volume-a"]; !ok || !reflect.DeepEqual(got, state) {
				t.Fatalf("failed temporary cleanup lost volume definition: %#v", driver.volumes)
			}
			if driver.recoveryIssues["volume-a"] == nil || driver.recoveryIssues["volume-a"].Error() != err.Error() {
				t.Fatalf("recovery issue = %v, want mount error %v", driver.recoveryIssues["volume-a"], err)
			}
			if len(preparer.snapshot()) != 1 {
				t.Fatalf("subdirectory preparer calls = %#v, want one safe preparation before cleanup", preparer.snapshot())
			}
			if probe.count() != 2 {
				t.Fatalf("health probes = %d, want post-command and pre-unmount probes", probe.count())
			}
		})
	}
}

type regularUnmountRevalidationCase struct {
	name   string
	result func(string, volumeState, mountRecord) mountInfoResult
}

func regularUnmountRevalidationCases() []regularUnmountRevalidationCase {
	return []regularUnmountRevalidationCase{
		{
			name: "mountinfo becomes unreadable",
			result: func(_ string, _ volumeState, _ mountRecord) mountInfoResult {
				return mountInfoResult{err: errors.New("mountinfo read failed during revalidation")}
			},
		},
		{
			name: "accepted mount disappears",
			result: func(_ string, _ volumeState, _ mountRecord) mountInfoResult {
				return mountInfoResult{}
			},
		},
		{
			name: "duplicate stack appears",
			result: func(target string, state volumeState, accepted mountRecord) mountInfoResult {
				duplicate := intendedRecord(target, state, "", accepted.mountID+1)
				return mountInfoResult{records: []mountRecord{accepted, duplicate}}
			},
		},
		{
			name: "nested mount appears",
			result: func(target string, _ volumeState, accepted mountRecord) mountInfoResult {
				nested := mountRecord{mountID: accepted.mountID + 1, target: filepath.Join(target, "unknown-nested"), filesystem: "tmpfs", source: "tmpfs"}
				return mountInfoResult{records: []mountRecord{accepted, nested}}
			},
		},
		{
			name: "accepted record is replaced",
			result: func(target string, state volumeState, accepted mountRecord) mountInfoResult {
				replacement := intendedRecord(target, state, "", accepted.mountID+1)
				replacement.source = "unknown-server:replacement-volume"
				return mountInfoResult{records: []mountRecord{replacement}}
			},
		},
	}
}

func assertActionableRegularUnmountRevalidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("regular unmount unexpectedly succeeded after mount state changed")
	}
	for _, expected := range []string{
		"regular unmount identity revalidation",
		"performed no unmount",
		"next action:",
		"inspect the target",
		"retry",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("error %q missing actionable detail %q", err, expected)
		}
	}
}

func assertConnectorDidNotUnmount(t *testing.T, client *fakeConnector) {
	t.Helper()
	mounts, unmounts, lazy := client.counts()
	if mounts != 0 || unmounts != 0 || lazy != 0 {
		t.Fatalf("fail-closed path invoked connector: mount=%d unmount=%d lazy=%d", mounts, unmounts, lazy)
	}
}
