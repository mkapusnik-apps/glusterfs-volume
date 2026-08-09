package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/go-plugins-helpers/volume"
)

func TestPersistedInvalidStateIsBlockedBeforeTargetSideEffects(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "volumes.json")
	outside := filepath.Clean(filepath.Join(root, "..", "escaped-volume"))
	_ = os.RemoveAll(outside)

	legacy := map[string]volumeState{
		"../escaped-volume": {
			Name:    "../escaped-volume",
			Servers: []string{"server-a"},
			Volume:  "gv0",
		},
		"name-mismatch": {
			Name:    "different-name",
			Servers: []string{"server-a"},
			Volume:  "gv0",
		},
		"bad-subdir": {
			Name:    "bad-subdir",
			Servers: []string{"server-a"},
			Volume:  "gv0",
			Subdir:  "../../outside",
		},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStateStore(statePath)
	loaded, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	table := &fakeMountInfo{}
	client := &fakeConnector{}
	probe := &fakeProbe{}
	driver := &glusterfsDriver{
		root:           root,
		store:          store,
		volumes:        loaded,
		mounts:         map[string]*activeMount{},
		recoveryIssues: map[string]error{},
		client:         client,
		mountInfo:      table,
		healthProbe:    probe,
	}

	driver.reconcileStartupContext(context.Background())

	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("corrupt traversal state affected outside target %s: %v", outside, err)
	}
	if mounts, unmounts, lazy := client.counts(); mounts != 0 || unmounts != 0 || lazy != 0 {
		t.Fatalf("invalid state caused connector side effects: mount=%d unmount=%d lazy=%d", mounts, unmounts, lazy)
	}
	if probe.count() != 0 {
		t.Fatalf("invalid state caused %d health probes", probe.count())
	}
	// The only mount-table read is the final unknown-mount warning scan.
	if table.readCount() != 1 {
		t.Fatalf("mount table reads = %d, want only final warning scan", table.readCount())
	}
	for name := range legacy {
		issue := driver.recoveryIssues[name]
		if issue == nil || !strings.Contains(issue.Error(), "invalid managed volume definition") || !strings.Contains(issue.Error(), "performed no filesystem or mount operation") {
			t.Errorf("issue for %q = %v", name, issue)
		}
	}
}

func TestCreateRejectsUnsafeManagedNamesWithoutPersisting(t *testing.T) {
	tests := []string{"../escape", "/absolute", ".glusterfs-plugin", "nested/name", "line\nbreak"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			driver, table, client, probe := newTestDriver(t, nil)
			err := driver.Create(&volume.CreateRequest{
				Name:    name,
				Options: map[string]string{"server": "server-a", "volume": "gv0"},
			})
			if err == nil {
				t.Fatalf("unsafe name %q unexpectedly succeeded", name)
			}
			if len(driver.volumes) != 0 {
				t.Fatalf("unsafe name was persisted in memory: %#v", driver.volumes)
			}
			if table.readCount() != 0 || probe.count() != 0 {
				t.Fatal("unsafe create inspected mount state")
			}
			if mounts, unmounts, lazy := client.counts(); mounts+unmounts+lazy != 0 {
				t.Fatal("unsafe create invoked mount connector")
			}
		})
	}
}

func TestValidatedTargetRequiresAbsoluteRootAndSingleSafeComponent(t *testing.T) {
	driver, _, _, _ := newTestDriver(t, nil)
	state := testState("safe-name", "")
	target, err := driver.validatedTarget("safe-name", state)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(driver.root, "safe-name") {
		t.Fatalf("target = %q", target)
	}

	driver.root = "relative-root"
	if _, err := driver.validatedTarget("safe-name", state); err == nil || !strings.Contains(err.Error(), "invalid managed root") {
		t.Fatalf("relative root error = %v", err)
	}
}
