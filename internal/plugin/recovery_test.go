package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReconcileTargetRequiresExactConfiguredMountIdentity(t *testing.T) {
	tests := []struct {
		name       string
		subdir     string
		record     func(string, volumeState) mountRecord
		wantMount  bool
		wantError  string
		wantProbes int
	}{
		{
			name: "correct root",
			record: func(target string, state volumeState) mountRecord {
				return intendedRecord(target, state, "", 10)
			},
			wantMount:  true,
			wantProbes: 1,
		},
		{
			name:   "correct subdirectory",
			subdir: "configs/speed",
			record: func(target string, state volumeState) mountRecord {
				return intendedRecord(target, state, state.Subdir, 11)
			},
			wantMount:  true,
			wantProbes: 1,
		},
		{
			name: "wrong volume",
			record: func(target string, state volumeState) mountRecord {
				record := intendedRecord(target, state, "", 12)
				record.source = "server-a:other-volume"
				return record
			},
			wantError: "conflicting or temporary mount identity",
		},
		{
			name:   "wrong subdirectory",
			subdir: "configs/speed",
			record: func(target string, state volumeState) mountRecord {
				record := intendedRecord(target, state, "other/path", 13)
				return record
			},
			wantError: "conflicting or temporary mount identity",
		},
		{
			name:   "temporary root mount is not adopted as subdirectory",
			subdir: "configs/speed",
			record: func(target string, state volumeState) mountRecord {
				return intendedRecord(target, state, "", 14)
			},
			wantError: "conflicting or temporary mount identity",
		},
		{
			name: "unverifiable root",
			record: func(target string, state volumeState) mountRecord {
				record := intendedRecord(target, state, "", 15)
				record.root = "/unknown-root"
				return record
			},
			wantError: "conflicting or temporary mount identity",
		},
		{
			name: "read-only identity",
			record: func(target string, state volumeState) mountRecord {
				record := intendedRecord(target, state, "", 16)
				record.mountOptions = "ro"
				return record
			},
			wantError: "conflicting or temporary mount identity",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := testState("volume-a", test.subdir)
			driver, table, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
			target := filepath.Join(driver.root, "volume-a")
			table.set(test.record(target, state))

			mounted, err := driver.reconcileTarget(context.Background(), "volume-a", target, state)
			if mounted != test.wantMount {
				t.Fatalf("mounted = %v, want %v (error %v)", mounted, test.wantMount, err)
			}
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
			if probe.count() != test.wantProbes {
				t.Fatalf("probe calls = %d, want %d", probe.count(), test.wantProbes)
			}
			if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
				t.Fatalf("identity reconciliation changed mounts: %d/%d/%d", mounts, unmounts, lazy)
			}
		})
	}
}

func TestStaleCandidateIdentityChangeBeforeDetachPreservesReplacement(t *testing.T) {
	state := testState("volume-a", "")
	driver, _, client, probe := newTestDriver(t, map[string]volumeState{"volume-a": state})
	target := filepath.Join(driver.root, "volume-a")
	candidate := intendedRecord(target, state, "", 17)
	replacement := intendedRecord(target, state, "", 18)
	replacement.source = "server-b:replacement-volume"
	driver.mountInfo = &scriptedMountInfo{results: []mountInfoResult{
		{records: []mountRecord{candidate}},
		{records: []mountRecord{replacement}},
	}}
	probe.err = syscall.ENOTCONN

	mounted, err := driver.reconcileTarget(context.Background(), "volume-a", target, state)
	if mounted || err == nil || !strings.Contains(err.Error(), "preserved changed or ambiguous mount state") {
		t.Fatalf("changed identity result mounted=%v error=%v", mounted, err)
	}
	if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
		t.Fatalf("replacement mount was modified: mount=%d unmount=%d lazy=%d", mounts, unmounts, lazy)
	}
}

func TestProcMountInfoReaderParsesIndependentGlusterRootAndSubdirectoryFixtures(t *testing.T) {
	fixture := strings.Join([]string{
		`101 55 0:42 / /var/lib/glusterfs-volume/root\040volume rw,nosuid,nodev,relatime shared:12 - fuse.glusterfs server-a:gv0 rw,user_id=0,group_id=0`,
		`102 55 0:43 / /var/lib/glusterfs-volume/subdir\040volume rw,nosuid,nodev,relatime master:1 - fuse.glusterfs server-b:gv0/configs/speed rw,user_id=0,group_id=0`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	records, err := (procMountInfoReader{path: path}).read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	root := records[0]
	if root.mountID != 101 || root.parentID != 55 || root.majorMinor != "0:42" || root.root != "/" || root.target != "/var/lib/glusterfs-volume/root volume" || root.filesystem != "fuse.glusterfs" || root.source != "server-a:gv0" || root.optionalFields != "shared:12" {
		t.Fatalf("parsed direct-root fixture = %#v", root)
	}
	subdir := records[1]
	if subdir.mountID != 102 || subdir.majorMinor != "0:43" || subdir.target != "/var/lib/glusterfs-volume/subdir volume" || subdir.source != "server-b:gv0/configs/speed" || subdir.optionalFields != "master:1" {
		t.Fatalf("parsed subdirectory fixture = %#v", subdir)
	}
	rootState := volumeState{Name: "root volume", Servers: []string{"server-a"}, Volume: "gv0"}
	if !recordMatchesExpected(root, rootState, "") {
		t.Fatal("literal direct-root fixture did not match its independently asserted identity")
	}
	subdirState := volumeState{Name: "subdir volume", Servers: []string{"server-b"}, Volume: "gv0", Subdir: "configs/speed"}
	if !recordMatchesExpected(subdir, subdirState, "configs/speed") {
		t.Fatal("literal subdirectory fixture did not match its independently asserted identity")
	}
}

func TestSubprocessMountHealthProbeExitMappingAndTimeout(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantError error
		wantText  string
	}{
		{name: "healthy", exitCode: probeExitHealthy},
		{name: "not connected", exitCode: probeExitNotConnected, wantError: syscall.ENOTCONN},
		{name: "stale", exitCode: probeExitStale, wantError: syscall.ESTALE},
		{name: "IO", exitCode: probeExitIO, wantError: syscall.EIO},
		{name: "other", exitCode: probeExitOther, wantText: "exited with status 4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PLUGIN_TEST_PROBE_SLEEP", "")
			t.Setenv("PLUGIN_TEST_PROBE_EXIT", strconv.Itoa(test.exitCode))
			err := (subprocessMountHealthProbe{timeout: time.Second}).probe(context.Background(), "/unused-test-target")
			if test.wantError == nil && test.wantText == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if test.wantText != "" && (err == nil || !strings.Contains(err.Error(), test.wantText)) {
				t.Fatalf("error = %v, want substring %q", err, test.wantText)
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		t.Setenv("PLUGIN_TEST_PROBE_EXIT", strconv.Itoa(probeExitHealthy))
		t.Setenv("PLUGIN_TEST_PROBE_SLEEP", "200ms")
		err := (subprocessMountHealthProbe{timeout: 10 * time.Millisecond}).probe(context.Background(), "/unused-test-target")
		if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "mount health probe exceeded") {
			t.Fatalf("timeout error = %v", err)
		}
	})
}

func TestStartupReconciliationOutcomes(t *testing.T) {
	t.Run("healthy intended mount is preserved", func(t *testing.T) {
		state := testState("healthy", "")
		driver, table, client, probe := newTestDriver(t, map[string]volumeState{"healthy": state})
		target := filepath.Join(driver.root, "healthy")
		table.set(intendedRecord(target, state, "", 20))

		driver.reconcileStartupContext(context.Background())

		mount := driver.mounts["healthy"]
		if mount == nil || mount.mountpoint != target || mount.connections != 0 {
			t.Fatalf("recovered mount = %#v", mount)
		}
		if driver.recoveryIssues["healthy"] != nil || probe.count() != 1 {
			t.Fatalf("healthy recovery issue=%v probes=%d", driver.recoveryIssues["healthy"], probe.count())
		}
		if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
			t.Fatal("healthy mount was disrupted")
		}
	})

	t.Run("missing mount prepares directory", func(t *testing.T) {
		state := testState("missing", "")
		driver, _, client, probe := newTestDriver(t, map[string]volumeState{"missing": state})
		target := filepath.Join(driver.root, "missing")

		driver.reconcileStartupContext(context.Background())

		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			t.Fatalf("prepared target info=%v err=%v", info, err)
		}
		if len(driver.mounts) != 0 || driver.recoveryIssues["missing"] != nil || probe.count() != 0 {
			t.Fatalf("missing outcome mounts=%#v issue=%v probes=%d", driver.mounts, driver.recoveryIssues["missing"], probe.count())
		}
		if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
			t.Fatal("missing mount invoked connector")
		}
	})

	for _, staleErr := range []error{syscall.ENOTCONN, syscall.ESTALE, syscall.EIO} {
		t.Run("stale "+staleErr.Error()+" is safely detached", func(t *testing.T) {
			state := testState("stale", "")
			driver, table, client, probe := newTestDriver(t, map[string]volumeState{"stale": state})
			target := filepath.Join(driver.root, "stale")
			table.set(intendedRecord(target, state, "", 21))
			probe.err = staleErr
			client.lazyFn = func(_ context.Context, _ string) error {
				table.set()
				return nil
			}

			driver.reconcileStartupContext(context.Background())

			if len(table.snapshot()) != 0 || len(driver.mounts) != 0 || driver.recoveryIssues["stale"] != nil {
				t.Fatalf("stale outcome records=%#v mounts=%#v issue=%v", table.snapshot(), driver.mounts, driver.recoveryIssues["stale"])
			}
			_, _, lazy := client.counts()
			if lazy != 1 {
				t.Fatalf("lazy unmounts = %d, want 1", lazy)
			}
		})
	}

	t.Run("expired startup deadline performs no volume operation", func(t *testing.T) {
		state := testState("timed-out", "")
		driver, table, client, probe := newTestDriver(t, map[string]volumeState{"timed-out": state})
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		driver.reconcileStartupContext(ctx)

		issue := driver.recoveryIssues["timed-out"]
		if issue == nil || !strings.Contains(issue.Error(), "startup recovery deadline reached") || !strings.Contains(issue.Error(), "performed no operation") {
			t.Fatalf("timeout issue = %v", issue)
		}
		if _, err := os.Stat(filepath.Join(driver.root, "timed-out")); !os.IsNotExist(err) {
			t.Fatalf("timeout unexpectedly prepared target: %v", err)
		}
		if probe.count() != 0 {
			t.Fatal("timeout probed mount")
		}
		if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
			t.Fatal("timeout invoked connector")
		}
		// warnUnknownMounts invokes the reader with the already-expired context,
		// which is rejected before the fake's read counter increments.
		if table.readCount() != 0 {
			t.Fatalf("timeout read mount table %d times", table.readCount())
		}
	})

	t.Run("duplicate stack is preserved as ambiguous", func(t *testing.T) {
		state := testState("duplicate", "")
		driver, table, client, probe := newTestDriver(t, map[string]volumeState{"duplicate": state})
		target := filepath.Join(driver.root, "duplicate")
		table.set(intendedRecord(target, state, "", 22), intendedRecord(target, state, "", 23))

		driver.reconcileStartupContext(context.Background())

		issue := driver.recoveryIssues["duplicate"]
		if issue == nil || !strings.Contains(issue.Error(), "ambiguous duplicate mount stack") || !strings.Contains(issue.Error(), "preserved every mount") {
			t.Fatalf("duplicate issue = %v", issue)
		}
		if len(table.snapshot()) != 2 || probe.count() != 0 {
			t.Fatalf("duplicate stack changed or probed: %#v", table.snapshot())
		}
		if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
			t.Fatal("duplicate stack was modified")
		}
	})

	t.Run("unknown mount is warned and preserved", func(t *testing.T) {
		driver, table, client, _ := newTestDriver(t, nil)
		unknown := mountRecord{target: filepath.Join(driver.root, "unknown"), filesystem: "tmpfs", source: "tmpfs"}
		table.set(unknown)

		logs := captureLogs(t, func() { driver.reconcileStartupContext(context.Background()) })

		if !strings.Contains(logs, "preserved unknown mount target") || !strings.Contains(logs, unknown.target) {
			t.Fatalf("unknown mount warning missing:\n%s", logs)
		}
		if len(table.snapshot()) != 1 {
			t.Fatal("unknown mount was changed")
		}
		if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
			t.Fatal("unknown mount invoked connector")
		}
	})
}

func TestStartupOverallDeadlinePartwayThroughVolumesStopsLaterOperations(t *testing.T) {
	states := map[string]volumeState{
		"volume-a": testState("volume-a", ""),
		"volume-b": testState("volume-b", ""),
	}
	driver, _, client, probe := newTestDriver(t, states)
	ctx, cancel := context.WithCancel(context.Background())
	driver.mountInfo = &cancelAfterFirstMountInfo{cancel: cancel}

	driver.reconcileStartupContext(ctx)

	prepared := 0
	timedOut := 0
	for name := range states {
		if info, err := os.Stat(filepath.Join(driver.root, name)); err == nil && info.IsDir() {
			prepared++
		}
		if issue := driver.recoveryIssues[name]; issue != nil && strings.Contains(issue.Error(), "startup recovery deadline reached") {
			timedOut++
		}
	}
	if prepared != 1 || timedOut != 1 {
		t.Fatalf("partway deadline prepared=%d timedOut=%d issues=%#v", prepared, timedOut, driver.recoveryIssues)
	}
	if probe.count() != 0 {
		t.Fatalf("deadline path probed %d mounts", probe.count())
	}
	if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
		t.Fatalf("deadline path invoked connector: %d/%d/%d", mounts, unmounts, lazy)
	}
}

func TestPrepareMountpointPreservesExistingContentsAndRejectsIncompatibleObjects(t *testing.T) {
	state := testState("volume-a", "")

	t.Run("missing and empty directories are accepted", func(t *testing.T) {
		driver, _, _, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
		target := filepath.Join(driver.root, "volume-a")
		if err := driver.prepareMountpoint("volume-a", target, state); err != nil {
			t.Fatal(err)
		}
		if err := driver.prepareMountpoint("volume-a", target, state); err != nil {
			t.Fatalf("existing empty directory rejected: %v", err)
		}
	})

	t.Run("non-empty hidden contents are preserved and warned", func(t *testing.T) {
		driver, _, _, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
		target := filepath.Join(driver.root, "volume-a")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		hidden := filepath.Join(target, ".hidden-data")
		if err := os.WriteFile(hidden, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t, func() {
			if err := driver.prepareMountpoint("volume-a", target, state); err != nil {
				t.Fatal(err)
			}
		})
		assertFileContents(t, hidden, "preserve")
		if !strings.Contains(logs, "non-empty ordinary directory") || !strings.Contains(logs, "never modify or delete") {
			t.Fatalf("content warning missing:\n%s", logs)
		}
	})

	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind+" is preserved", func(t *testing.T) {
			driver, _, _, _ := newTestDriver(t, map[string]volumeState{"volume-a": state})
			target := filepath.Join(driver.root, "volume-a")
			if kind == "file" {
				if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				destination := filepath.Join(driver.root, "destination")
				if err := os.Mkdir(destination, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(destination, target); err != nil {
					t.Fatal(err)
				}
			}

			err := driver.prepareMountpoint("volume-a", target, state)
			if err == nil || !strings.Contains(err.Error(), "incompatible mountpoint object") || !strings.Contains(err.Error(), "preserved the object") {
				t.Fatalf("incompatible object error = %v", err)
			}
			if _, statErr := os.Lstat(target); statErr != nil {
				t.Fatalf("incompatible object removed: %v", statErr)
			}
			if kind == "file" {
				assertFileContents(t, target, "sentinel")
			}
		})
	}
}

func TestRecoveryDiagnosticsAreActionableAndDoNotIncludeUnrelatedSecrets(t *testing.T) {
	t.Setenv("UNRELATED_SECRET", "do-not-print-this-token")
	err := newRecoveryFailure(
		"volume-a",
		"/managed/volume-a",
		"intended mount with unproven health",
		"preserved the mount and returned no path",
		errors.New("probe permission denied"),
		"restore bounded probe access and retry",
	)
	message := err.Error()
	for _, required := range []string{
		`volume "volume-a"`,
		`target "/managed/volume-a"`,
		"detected intended mount with unproven health",
		"recovery result: preserved the mount and returned no path",
		"cause: probe permission denied",
		"next action: restore bounded probe access and retry",
	} {
		if !strings.Contains(message, required) {
			t.Errorf("diagnostic %q missing %q", message, required)
		}
	}
	if strings.Contains(message, os.Getenv("UNRELATED_SECRET")) {
		t.Fatalf("diagnostic leaked unrelated environment secret: %s", message)
	}
}
