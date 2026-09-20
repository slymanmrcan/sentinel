//go:build !linux

package monitor

import (
	"fmt"

	"github.com/shirou/gopsutil/v3/disk"
)

func filesystemMounts(hostProc string) ([]filesystemMount, error) {
	if hostProc != "" {
		return nil, fmt.Errorf("host mount discovery requires Linux")
	}
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}
	mounts := make([]filesystemMount, 0, len(partitions))
	for _, partition := range partitions {
		mounts = append(mounts, filesystemMount{device: partition.Device, mountpoint: partition.Mountpoint, fstype: partition.Fstype})
	}
	return mounts, nil
}

func filesystemUsage(path, _ string) (*disk.UsageStat, error) {
	return disk.Usage(path)
}
