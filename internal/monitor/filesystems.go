package monitor

import (
	"errors"
	"path/filepath"
	"sort"

	"github.com/shirou/gopsutil/v3/disk"
	"github.com/slymanmrcan/sentinel/internal/store"
)

type filesystemMount struct {
	device, mountpoint, fstype, deviceID string
}

func (c *Collector) filesystems() ([]store.Filesystem, error) {
	mounts, err := filesystemMounts(c.cfg.HostProc)
	if err != nil {
		return nil, err
	}
	return sampleFilesystems(mounts, c.cfg.HostRoot, filesystemUsage)
}

func sampleFilesystems(mounts []filesystemMount, hostRoot string, usage func(string, string) (*disk.UsageStat, error)) ([]store.Filesystem, error) {
	result := make([]store.Filesystem, 0, len(mounts))
	var sampleErr error
	for _, mount := range mounts {
		path := filepath.Join(hostRoot, mount.mountpoint)
		item := store.Filesystem{Device: mount.device, Mountpoint: mount.mountpoint, Fstype: mount.fstype}
		stat, err := usage(path, mount.deviceID)
		if err == nil && stat != nil && stat.Total > 0 {
			item.Total, item.Used, item.Available = stat.Total, stat.Used, stat.Free
			item.UsedPercent, item.AvailableStats = stat.UsedPercent, true
		} else {
			// A missing mount must never look like an empty, healthy disk.
			sampleErr = errors.New("some filesystem stats are unavailable")
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Mountpoint < result[j].Mountpoint })
	return result, sampleErr
}
