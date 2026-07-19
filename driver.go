package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/docker/go-plugins-helpers/volume"
)

const defaultMode = 0o755

type activeMount struct {
	connections int
	mountpoint  string
	createdAt   time.Time
	ids         map[string]int
}

type glusterfsDriver struct {
	sync.RWMutex

	root           string
	store          *stateStore
	volumes        map[string]volumeState
	mounts         map[string]*activeMount
	recoveryIssues map[string]error
	defaultVolume  string
	defaultServers []string
	client         glusterConnector
	mountInfo      mountInfoReader
	healthProbe    mountHealthProbe
}

func (d *glusterfsDriver) Create(r *volume.CreateRequest) error {
	d.Lock()
	defer d.Unlock()

	servers := splitList(r.Options["server"])
	name := r.Options["name"]
	if name == "" {
		name = r.Options["volume"]
	}
	if len(servers) == 0 {
		servers = d.defaultServers
	}
	if name == "" {
		name = d.defaultVolume
	}
	volumeName, subdir, err := parseGfsName(name)
	if err != nil {
		return err
	}
	if volumeName == "" || len(servers) == 0 {
		return fmt.Errorf("glusterfs options must include server and volume")
	}
	state := volumeState{
		Name:      r.Name,
		Servers:   servers,
		Volume:    volumeName,
		Subdir:    subdir,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := d.validatedTarget(r.Name, state); err != nil {
		return err
	}

	if _, ok := d.volumes[r.Name]; !ok {
		d.volumes[r.Name] = state
		if err := d.store.save(d.volumes); err != nil {
			delete(d.volumes, r.Name)
			return err
		}
	}

	return nil
}

func (d *glusterfsDriver) List() (*volume.ListResponse, error) {
	d.RLock()
	defer d.RUnlock()

	vols := make([]*volume.Volume, 0, len(d.volumes))
	for _, v := range d.volumes {
		vols = append(vols, &volume.Volume{Name: v.Name})
	}

	return &volume.ListResponse{Volumes: vols}, nil
}

func (d *glusterfsDriver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	d.RLock()
	defer d.RUnlock()

	state, ok := d.volumes[r.Name]
	if !ok {
		return &volume.GetResponse{}, fmt.Errorf("volume %s not found", r.Name)
	}

	status := map[string]interface{}{
		"servers": state.Servers,
		"volume":  state.Volume,
		"subdir":  state.Subdir,
	}
	if mount, ok := d.mounts[r.Name]; ok {
		status["mountpoint"] = mount.mountpoint
		status["references"] = mount.connections
	}
	if issue, ok := d.recoveryIssues[r.Name]; ok {
		status["recovery_issue"] = issue.Error()
	}

	vol := &volume.Volume{
		Name:       state.Name,
		CreatedAt:  state.CreatedAt,
		Mountpoint: state.Subdir,
		Status:     status,
	}

	return &volume.GetResponse{Volume: vol}, nil
}

func (d *glusterfsDriver) Remove(r *volume.RemoveRequest) error {
	d.Lock()
	defer d.Unlock()

	state, ok := d.volumes[r.Name]
	if !ok {
		return nil
	}
	if mount, ok := d.mounts[r.Name]; ok && mount.connections > 0 {
		return fmt.Errorf("volume %s is still mounted", r.Name)
	}
	target, err := d.validatedTarget(r.Name, state)
	if err != nil {
		delete(d.volumes, r.Name)
		delete(d.recoveryIssues, r.Name)
		if saveErr := d.store.save(d.volumes); saveErr != nil {
			d.volumes[r.Name] = state
			d.recoveryIssues[r.Name] = err
			return saveErr
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestOperationTimeout)
	defer cancel()
	mounted, err := d.reconcileTarget(ctx, r.Name, target, state)
	if err != nil {
		d.recoveryIssues[r.Name] = err
		return err
	}
	if mounted {
		if err := d.unmountPhysical(ctx, r.Name, target, state, state.Subdir); err != nil {
			d.recoveryIssues[r.Name] = err
			return err
		}
	}

	delete(d.volumes, r.Name)
	delete(d.mounts, r.Name)
	delete(d.recoveryIssues, r.Name)

	if err := d.store.save(d.volumes); err != nil {
		d.volumes[r.Name] = state
		return err
	}
	return nil
}

func (d *glusterfsDriver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	d.Lock()
	defer d.Unlock()

	mount, ok := d.mounts[r.Name]
	if !ok || mount.connections == 0 {
		return &volume.PathResponse{}, fmt.Errorf("no mountpoint for volume")
	}
	state, ok := d.volumes[r.Name]
	if !ok {
		return &volume.PathResponse{}, fmt.Errorf("volume %s not found", r.Name)
	}
	target, err := d.validatedTarget(r.Name, state)
	if err != nil || target != mount.mountpoint {
		if err == nil {
			err = fmt.Errorf("active mountpoint %q does not match validated target %q", mount.mountpoint, target)
		}
		return &volume.PathResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestOperationTimeout)
	defer cancel()
	mounted, err := d.reconcileTarget(ctx, r.Name, mount.mountpoint, state)
	if err != nil {
		d.recoveryIssues[r.Name] = err
		return &volume.PathResponse{}, err
	}
	if !mounted {
		err := newRecoveryFailure(
			r.Name, mount.mountpoint, "missing physical mount with active logical references",
			"no false-success path was returned", fmt.Errorf("%d logical reference(s) remain", mount.connections),
			"retry the mount request after resolving GlusterFS connectivity",
		)
		d.recoveryIssues[r.Name] = err
		return &volume.PathResponse{}, err
	}
	delete(d.recoveryIssues, r.Name)

	return &volume.PathResponse{Mountpoint: mount.mountpoint}, nil
}

func (d *glusterfsDriver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
	d.Lock()
	defer d.Unlock()

	state, ok := d.volumes[r.Name]
	if !ok {
		return &volume.MountResponse{}, fmt.Errorf("volume %s not found", r.Name)
	}

	mountpoint, err := d.validatedTarget(r.Name, state)
	if err != nil {
		d.recoveryIssues[r.Name] = err
		return &volume.MountResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestOperationTimeout)
	defer cancel()
	mounted, err := d.reconcileTarget(ctx, r.Name, mountpoint, state)
	if err != nil {
		d.recoveryIssues[r.Name] = err
		return &volume.MountResponse{}, err
	}
	if !mounted {
		if err := d.prepareMountpoint(r.Name, mountpoint, state); err != nil {
			d.recoveryIssues[r.Name] = err
			return &volume.MountResponse{}, err
		}
		if err := d.mountPhysical(ctx, r.Name, mountpoint, state); err != nil {
			d.recoveryIssues[r.Name] = err
			return &volume.MountResponse{}, err
		}
	}
	delete(d.recoveryIssues, r.Name)

	info, ok := d.mounts[r.Name]
	if !ok {
		info = &activeMount{mountpoint: mountpoint, ids: map[string]int{}, createdAt: time.Now().UTC()}
		d.mounts[r.Name] = info
	}

	info.mountpoint = mountpoint
	info.ids[r.ID]++
	info.connections++

	return &volume.MountResponse{Mountpoint: mountpoint}, nil
}

func (d *glusterfsDriver) Unmount(r *volume.UnmountRequest) error {
	d.Lock()
	defer d.Unlock()

	info, ok := d.mounts[r.Name]
	if !ok {
		return fmt.Errorf("volume not mounted: %s", r.Name)
	}
	if info.connections == 0 {
		return fmt.Errorf("volume has no active mounts: %s", r.Name)
	}
	count, ok := info.ids[r.ID]
	if !ok {
		return fmt.Errorf("mount %s does not know about client %s", r.Name, r.ID)
	}

	if info.connections == 1 {
		log.Printf("Unmounting volume %s", r.Name)
		state, ok := d.volumes[r.Name]
		if !ok {
			return fmt.Errorf("volume %s not found", r.Name)
		}
		target, err := d.validatedTarget(r.Name, state)
		if err != nil || target != info.mountpoint {
			if err == nil {
				err = fmt.Errorf("active mountpoint %q does not match validated target %q", info.mountpoint, target)
			}
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestOperationTimeout)
		defer cancel()
		if err := d.unmountPhysical(ctx, r.Name, info.mountpoint, state, state.Subdir); err != nil {
			d.recoveryIssues[r.Name] = err
			return err
		}
		delete(d.recoveryIssues, r.Name)
		delete(d.mounts, r.Name)
		return nil
	}

	count--
	info.connections--
	if count <= 0 {
		delete(info.ids, r.ID)
	} else {
		info.ids[r.ID] = count
	}

	return nil
}

func (d *glusterfsDriver) Capabilities() *volume.CapabilitiesResponse {
	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "global"}}
}
