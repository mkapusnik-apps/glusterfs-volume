package plugin

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == internalMountProbeArgument {
		if delay := os.Getenv("PLUGIN_TEST_PROBE_SLEEP"); delay != "" {
			duration, err := time.ParseDuration(delay)
			if err != nil {
				os.Exit(probeExitOther)
			}
			time.Sleep(duration)
		}
		if code := os.Getenv("PLUGIN_TEST_PROBE_EXIT"); code != "" {
			value, err := strconv.Atoi(code)
			if err != nil {
				os.Exit(probeExitOther)
			}
			os.Exit(value)
		}
		os.Exit(runInternalMountProbe(os.Args[2]))
	}
	os.Exit(m.Run())
}

type fakeMountInfo struct {
	mu      sync.Mutex
	records []mountRecord
	reads   int
	errors  map[int]error
}

func (f *fakeMountInfo) read(ctx context.Context) ([]mountRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if err := f.errors[f.reads]; err != nil {
		return nil, err
	}
	return append([]mountRecord(nil), f.records...), nil
}

func (f *fakeMountInfo) set(records ...mountRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append([]mountRecord(nil), records...)
}

func (f *fakeMountInfo) snapshot() []mountRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mountRecord(nil), f.records...)
}

func (f *fakeMountInfo) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

type mountCall struct {
	target  string
	volume  string
	servers []string
	subdir  string
}

type fakeConnector struct {
	mu sync.Mutex

	mountCalls   []mountCall
	unmounts     []string
	lazyUnmounts []string

	mountFn   func(context.Context, string, string, []string, string) error
	unmountFn func(context.Context, string) error
	lazyFn    func(context.Context, string) error
}

func (f *fakeConnector) mountWithGlusterfs(ctx context.Context, target, volume string, servers []string, subdir string) error {
	f.mu.Lock()
	f.mountCalls = append(f.mountCalls, mountCall{target: target, volume: volume, servers: append([]string(nil), servers...), subdir: subdir})
	fn := f.mountFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, target, volume, servers, subdir)
	}
	return nil
}

func (f *fakeConnector) unmount(ctx context.Context, target string) error {
	f.mu.Lock()
	f.unmounts = append(f.unmounts, target)
	fn := f.unmountFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, target)
	}
	return nil
}

func (f *fakeConnector) unmountLazy(ctx context.Context, target string) error {
	f.mu.Lock()
	f.lazyUnmounts = append(f.lazyUnmounts, target)
	fn := f.lazyFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, target)
	}
	return nil
}

func (f *fakeConnector) counts() (mounts, unmounts, lazy int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mountCalls), len(f.unmounts), len(f.lazyUnmounts)
}

func (f *fakeConnector) mountedSubdirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, len(f.mountCalls))
	for index, call := range f.mountCalls {
		result[index] = call.subdir
	}
	return result
}

type fakeProbe struct {
	mu    sync.Mutex
	err   error
	errs  map[string]error
	calls []string
}

type subdirectoryCall struct {
	target   string
	subdir   string
	expected mountRecord
}

type fakeSubdirectoryPreparer struct {
	mu    sync.Mutex
	calls []subdirectoryCall
	err   error
}

func (f *fakeSubdirectoryPreparer) prepare(_ context.Context, target, subdir string, expected mountRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, subdirectoryCall{target: target, subdir: subdir, expected: expected})
	return f.err
}

func (f *fakeSubdirectoryPreparer) snapshot() []subdirectoryCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subdirectoryCall(nil), f.calls...)
}

type mountInfoResult struct {
	records []mountRecord
	err     error
}

type scriptedMountInfo struct {
	mu      sync.Mutex
	results []mountInfoResult
	reads   int
}

type cancelAfterFirstMountInfo struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterFirstMountInfo) read(ctx context.Context) ([]mountRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.reads == 1 {
		r.cancel()
	}
	return nil, nil
}

func (s *scriptedMountInfo) read(ctx context.Context) ([]mountRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.reads
	s.reads++
	if len(s.results) == 0 {
		return nil, nil
	}
	if index >= len(s.results) {
		index = len(s.results) - 1
	}
	result := s.results[index]
	return append([]mountRecord(nil), result.records...), result.err
}

func (f *fakeProbe) probe(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, target)
	if f.errs != nil {
		return f.errs[target]
	}
	return f.err
}

func (f *fakeProbe) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func testState(name, subdir string) volumeState {
	return volumeState{
		Name:      name,
		Servers:   []string{"server-a", "server-b"},
		Volume:    "gv0",
		Subdir:    subdir,
		CreatedAt: "2026-07-20T00:00:00Z",
	}
}

func intendedRecord(target string, state volumeState, subdir string, id int) mountRecord {
	return mountRecord{
		mountID:      id,
		parentID:     1,
		majorMinor:   "0:42",
		root:         "/",
		target:       target,
		mountOptions: "rw,nosuid",
		filesystem:   "fuse.glusterfs",
		source:       expectedMountSources(state, subdir)[0],
		superOptions: "rw,relatime",
	}
}

func newTestDriver(t *testing.T, states map[string]volumeState) (*glusterfsDriver, *fakeMountInfo, *fakeConnector, *fakeProbe) {
	t.Helper()
	root := t.TempDir()
	table := &fakeMountInfo{}
	client := &fakeConnector{}
	probe := &fakeProbe{}
	if states == nil {
		states = map[string]volumeState{}
	}
	driver := &glusterfsDriver{
		root:           root,
		store:          newStateStore(filepath.Join(t.TempDir(), "volumes.json")),
		volumes:        states,
		mounts:         map[string]*activeMount{},
		recoveryIssues: map[string]error{},
		client:         client,
		mountInfo:      table,
		healthProbe:    probe,
	}
	return driver, table, client, probe
}

func installSuccessfulMountBehavior(table *fakeMountInfo, client *fakeConnector, state volumeState) {
	nextID := 100
	client.mountFn = func(_ context.Context, target, _ string, _ []string, subdir string) error {
		nextID++
		table.set(intendedRecord(target, state, subdir, nextID))
		return nil
	}
	client.unmountFn = func(_ context.Context, _ string) error {
		table.set()
		return nil
	}
	client.lazyFn = func(_ context.Context, _ string) error {
		table.set()
		return nil
	}
}

func captureLogs(t *testing.T, action func()) string {
	t.Helper()
	var output bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})
	action()
	return output.String()
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != want {
		t.Fatalf("contents of %s = %q, want %q", path, contents, want)
	}
}
