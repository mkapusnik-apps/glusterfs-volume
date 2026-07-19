package main

import (
	"errors"
	"os"
	"strings"
)

func splitList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	var out []string
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseGfsName(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("glusterfs name is required")
	}
	if strings.Trim(raw, "/") != raw || strings.ContainsRune(raw, '\\') || containsControl(raw) {
		return "", "", errors.New("glusterfs name must be a relative slash-separated path")
	}
	parts := strings.Split(raw, "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", errors.New("invalid glusterfs name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("invalid glusterfs path component")
		}
	}
	volume := parts[0]
	if err := validateGlusterComponent(volume, "volume"); err != nil {
		return "", "", err
	}
	if len(parts) == 1 {
		return volume, "", nil
	}
	subdir := strings.Join(parts[1:], "/")
	if err := validateSubdirectory(subdir); err != nil {
		return "", "", err
	}
	return volume, subdir, nil
}

func ensureDirPath(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return errors.New("path exists and is not a directory")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, mode)
}
