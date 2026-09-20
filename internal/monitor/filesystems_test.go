package monitor

import (
	"errors"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func TestSampleFilesystemsMapsHostPathsAndKeepsDisksSeparate(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	mounts := []filesystemMount{
		{device: "/dev/sdb1", mountpoint: "/mnt/block", fstype: "ext4", deviceID: "8:17"},
		{device: "/dev/sda1", mountpoint: "/", fstype: "ext4", deviceID: "8:1"},
	}
	items, err := sampleFilesystems(mounts, "/host/root", func(path, deviceID string) (*disk.UsageStat, error) {
		switch path {
		case "/host/root":
			return &disk.UsageStat{Total: 48 * gib, Used: 21 * gib, Free: 27 * gib, UsedPercent: 43}, nil
		case "/host/root/mnt/block":
			if deviceID != "8:17" {
				t.Fatalf("wrong device ID: %s", deviceID)
			}
			return &disk.UsageStat{Total: 96 * gib, Used: 65 * gib, Free: 27 * gib, UsedPercent: 71}, nil
		default:
			t.Fatalf("unexpected path %q", path)
			return nil, nil
		}
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("filesystems = %+v, %v", items, err)
	}
	if items[0].Mountpoint != "/" || items[0].Total != 48*gib || items[1].Total != 96*gib || items[1].Available != 27*gib || items[1].UsedPercent != 71 || !items[1].AvailableStats {
		t.Fatalf("incorrect filesystem usage: %+v", items)
	}
}

func TestSampleFilesystemsPreservesPartialResults(t *testing.T) {
	mounts := []filesystemMount{{mountpoint: "/"}, {mountpoint: "/mnt/block"}}
	items, err := sampleFilesystems(mounts, "", func(path, _ string) (*disk.UsageStat, error) {
		if path == "/mnt/block" {
			return nil, errors.New("not mounted")
		}
		return &disk.UsageStat{Total: 100}, nil
	})
	if err == nil || len(items) != 2 || !items[0].AvailableStats || items[1].AvailableStats {
		t.Fatalf("partial results = %+v, %v", items, err)
	}
}

func TestCurrentCopiesFilesystemSnapshot(t *testing.T) {
	c := &Collector{current: store.Metric{Timestamp: time.Now(), Filesystems: []store.Filesystem{{Mountpoint: "/mnt/block"}}}}
	metric := c.Current()
	metric.Filesystems[0].Mountpoint = "/changed"
	if c.Current().Filesystems[0].Mountpoint != "/mnt/block" {
		t.Fatal("Current mutated shared filesystem data")
	}
}
