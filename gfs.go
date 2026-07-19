package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
)

type glusterConnector interface {
	mountWithGlusterfs(context.Context, string, string, []string, string) error
	unmount(context.Context, string) error
	unmountLazy(context.Context, string) error
}

type glfsConnector struct{}

func (d *glfsConnector) mountWithGlusterfs(ctx context.Context, mountpoint string, volume string, hosts []string, subdir string) error {
	cmd := exec.CommandContext(ctx, "glusterfs")
	for _, server := range hosts {
		cmd.Args = append(cmd.Args, "--volfile-server", server)
	}
	cmd.Args = append(cmd.Args, "--volfile-id", volume)
	if subdir != "" {
		cmd.Args = append(cmd.Args, "--subdir-mount", filepath.Join("/", subdir))
	}
	cmd.Args = append(cmd.Args, mountpoint)
	log.Printf("Executing %v", cmd.Args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("glusterfs mount timed out or was cancelled: %w", ctx.Err())
		}
		return fmt.Errorf("glusterfs mount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *glfsConnector) unmount(ctx context.Context, mountpoint string) error {
	cmd := exec.CommandContext(ctx, "umount", mountpoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("umount timed out or was cancelled: %w", ctx.Err())
		}
		return fmt.Errorf("umount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *glfsConnector) unmountLazy(ctx context.Context, mountpoint string) error {
	cmd := exec.CommandContext(ctx, "umount", "--lazy", mountpoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lazy umount timed out or was cancelled: %w", ctx.Err())
		}
		return fmt.Errorf("lazy umount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
