package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type volumeState struct {
	Name      string   `json:"name"`
	Servers   []string `json:"servers"`
	Volume    string   `json:"volume"`
	Subdir    string   `json:"subdir"`
	CreatedAt string   `json:"created_at"`
}

type stateStore struct {
	path string
}

func newStateStore(path string) *stateStore {
	return &stateStore{path: path}
}

func (s *stateStore) load() (map[string]volumeState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]volumeState{}, nil
		}
		return nil, err
	}
	var payload map[string]volumeState
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]volumeState{}
	}
	return payload, nil
}

func (s *stateStore) save(state map[string]volumeState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func (d *glusterfsDriver) validatedTarget(key string, state volumeState) (string, error) {
	if err := validateVolumeState(key, state); err != nil {
		return "", newRecoveryFailure(
			key, fmt.Sprintf("<not constructed beneath %q>", d.root),
			"invalid managed volume definition", "performed no filesystem or mount operation",
			err, "correct or remove the persisted definition, then restart or retry",
		)
	}
	root := filepath.Clean(d.root)
	if !filepath.IsAbs(root) {
		return "", newRecoveryFailure(
			key, "<not constructed>", "invalid managed root", "performed no filesystem or mount operation",
			fmt.Errorf("managed root %q is not absolute", d.root), "configure an absolute managed root before retrying",
		)
	}
	target := filepath.Clean(filepath.Join(root, key))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative != key || relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		if err == nil {
			err = fmt.Errorf("resolved relative path %q does not equal volume name", relative)
		}
		return "", newRecoveryFailure(
			key, target, "managed target escaped lexical confinement", "performed no filesystem or mount operation",
			err, "correct or remove the persisted volume name before retrying",
		)
	}
	return target, nil
}

func validateVolumeState(key string, state volumeState) error {
	if err := validateManagedName(key); err != nil {
		return fmt.Errorf("invalid persisted map key: %w", err)
	}
	if state.Name != key {
		return fmt.Errorf("persisted name %q does not match map key %q", state.Name, key)
	}
	if err := validateGlusterComponent(state.Volume, "volume"); err != nil {
		return err
	}
	if err := validateSubdirectory(state.Subdir); err != nil {
		return err
	}
	if len(state.Servers) == 0 {
		return errors.New("server list is empty")
	}
	for _, server := range state.Servers {
		if strings.TrimSpace(server) != server || server == "" || strings.ContainsAny(server, `/\\`) || containsControl(server) {
			return fmt.Errorf("invalid GlusterFS server identifier %q", server)
		}
	}
	return nil
}

func validateManagedName(name string) error {
	if name == "" || name == "." || name == ".." || name == ".glusterfs-plugin" {
		return fmt.Errorf("name %q is empty, reserved, or relative", name)
	}
	if filepath.IsAbs(name) || filepath.Base(name) != name || filepath.Clean(name) != name || strings.ContainsAny(name, `/\\`) || containsControl(name) {
		return fmt.Errorf("name %q is not one safe path component", name)
	}
	return nil
}

func validateGlusterComponent(value, kind string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) || containsControl(value) {
		return fmt.Errorf("invalid GlusterFS %s component %q", kind, value)
	}
	return nil
}

func validateSubdirectory(subdir string) error {
	if subdir == "" {
		return nil
	}
	if filepath.IsAbs(subdir) || filepath.Clean(subdir) != subdir || strings.ContainsRune(subdir, '\\') || containsControl(subdir) {
		return fmt.Errorf("invalid GlusterFS subdirectory %q", subdir)
	}
	for _, component := range strings.Split(subdir, "/") {
		if err := validateGlusterComponent(component, "subdirectory"); err != nil {
			return err
		}
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func isLexicallyBeneath(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
