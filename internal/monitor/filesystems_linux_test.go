//go:build linux

package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFilesystemMountsUsesHostNamespace(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "1"), 0755); err != nil {
		t.Fatal(err)
	}
	fixture := `1 0 8:1 / / rw - ext4 /dev/sda1 rw
2 1 0:20 / /run rw - tmpfs tmpfs rw
3 1 8:16 / /boot rw - ext4 /dev/sda16 rw
4 1 8:15 / /boot/efi rw - vfat /dev/sda15 rw
5 1 8:17 / /mnt/block rw shared:1 - ext4 /dev/sdb1 rw
6 1 0:21 / /var/lib/docker/overlay2/merged rw - overlay overlay rw
7 1 7:1 / /snap/core ro - squashfs /dev/loop1 ro
8 1 8:1 /var/lib/docker /container-bind rw - ext4 /dev/sda1 rw
9 1 8:18 / /mnt/with\040space rw - xfs /dev/sdc1 rw
`
	path := filepath.Join(proc, "1/mountinfo")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	mounts, err := filesystemMounts(proc)
	if err != nil || len(mounts) != 5 {
		t.Fatalf("mounts = %+v, %v", mounts, err)
	}
	if mounts[3].mountpoint != "/mnt/block" || mounts[3].deviceID != "8:17" || mounts[4].mountpoint != "/mnt/with space" {
		t.Fatalf("wrong mounts: %+v", mounts)
	}
	if _, err := filesystemMounts(t.TempDir()); err == nil {
		t.Fatal("missing host mountinfo must not fall back to container mounts")
	}
}

func TestParseFilesystemMountsRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"bad", "1 - ext4 /dev/sda1 rw", "1 0 8:1 / / rw - ext4"} {
		if _, err := parseFilesystemMounts(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestFilesystemUsageRejectsWrongDevice(t *testing.T) {
	path := t.TempDir()
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
	if usage, err := filesystemUsage(path, id); err != nil || usage.Total == 0 {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
	if _, err := filesystemUsage(path, "999:999"); err == nil {
		t.Fatal("accepted parent filesystem as block storage")
	}
}
