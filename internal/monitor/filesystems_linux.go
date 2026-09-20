//go:build linux

package monitor

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v3/disk"
	"golang.org/x/sys/unix"
)

func filesystemMounts(hostProc string) ([]filesystemMount, error) {
	path := "/proc/self/mountinfo"
	if hostProc != "" {
		// self would describe Sentinel's container mount namespace.
		path = filepath.Join(hostProc, "1/mountinfo")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseFilesystemMounts(file)
}

func parseFilesystemMounts(reader io.Reader) ([]filesystemMount, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	mounts := make([]filesystemMount, 0)
	seen := make(map[string]bool)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for scanner.Scan() {
		before, after, ok := strings.Cut(scanner.Text(), " - ")
		fields, fs := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 6 || len(fs) < 3 {
			return nil, fmt.Errorf("invalid mountinfo record")
		}
		device, mountpoint := unescape.Replace(fs[1]), unescape.Replace(fields[4])
		// Keep disk-backed filesystems; omit tmpfs, pseudo filesystems,
		// container overlays, loop images and bind mounts of subdirectories.
		if !strings.HasPrefix(device, "/dev/") || strings.HasPrefix(device, "/dev/loop") ||
			fs[0] == "squashfs" || (fields[3] != "/" && fs[0] != "btrfs") {
			continue
		}
		if seen[mountpoint] {
			continue
		}
		seen[mountpoint] = true
		mounts = append(mounts, filesystemMount{device: device, mountpoint: mountpoint, fstype: fs[0], deviceID: fields[2]})
	}
	return mounts, scanner.Err()
}

func filesystemUsage(path, deviceID string) (*disk.UsageStat, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return nil, err
	}
	actual := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
	if actual != deviceID {
		// A host mount not propagated into Docker can resolve to the parent
		// filesystem. Never label the root disk's capacity as block storage.
		return nil, fmt.Errorf("filesystem device mismatch at %s", path)
	}
	return disk.Usage(path)
}
