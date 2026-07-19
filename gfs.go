package main

import (
	"fmt"
	"log"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strings"
)

type glfsConnector struct{}

func (d *glfsConnector) mountWithGlusterfs(mountpoint string, volume string, hosts []string, subdir string) error {
	cmd := exec.Command("glusterfs")
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
		return fmt.Errorf("glusterfs mount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *glfsConnector) mountWithMount(mountpoint string, volume string, hosts []string, subdir string) error {
	cmd := exec.Command("mount")
	cmd.Args = append(cmd.Args, "-t", "glusterfs")
	server := hosts[rand.Intn(len(hosts))]
	path := volume
	if subdir != "" {
		path = filepath.Join(volume, subdir)
	}
	url := fmt.Sprintf("%s:/%s", server, path)
	cmd.Args = append(cmd.Args, url, mountpoint)
	log.Printf("Executing %v", cmd.Args)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount -t glusterfs failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *glfsConnector) unmount(mountpoint string) error {
	cmd := exec.Command("umount", mountpoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *glfsConnector) unmountLazy(mountpoint string) error {
	cmd := exec.Command("umount", "--lazy", mountpoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lazy umount failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
