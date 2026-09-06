package monitor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gnet "github.com/shirou/gopsutil/v3/net"
)

func (c *Collector) networkCounters() ([]gnet.IOCountersStat, error) {
	if c.cfg.HostProc == "" {
		return gnet.IOCounters(true)
	}
	// /proc/net -> self/net follows Sentinel's network namespace, even with
	// pid: host. The mounted host PID 1 explicitly selects the host namespace.
	return readHostNetworkCounters(c.cfg.HostProc)
}

func readHostNetworkCounters(root string) ([]gnet.IOCountersStat, error) {
	file, err := os.Open(filepath.Join(root, "1/net/dev"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]gnet.IOCountersStat, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 16 {
			return nil, fmt.Errorf("invalid host network counters")
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			return nil, fmt.Errorf("invalid host network bytes")
		}
		result = append(result, gnet.IOCountersStat{Name: strings.TrimSpace(line[:colon]), BytesRecv: rx, BytesSent: tx})
	}
	return result, scanner.Err()
}

// Only sockets in the host namespace are listed. PID attribution is best effort:
// unreadable process FDs leave PID=0/Unknown instead of hiding the listener.
func readHostListeningPorts(root string) ([]PortInfo, error) {
	type listener struct {
		port  uint32
		inode string
	}
	listeners := make([]listener, 0)
	for _, table := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(root, "1/net", table))
		if os.IsNotExist(err) && table == "tcp6" {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) < 10 {
				return nil, fmt.Errorf("invalid host TCP table")
			}
			if fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			port, err := strconv.ParseUint(portHex, 16, 16)
			if !ok || err != nil {
				return nil, fmt.Errorf("invalid host listening port")
			}
			listeners = append(listeners, listener{uint32(port), fields[9]})
		}
	}
	byInode := make(map[string]PortInfo, len(listeners))
	for _, item := range listeners {
		byInode[item.inode] = PortInfo{Port: item.port, Name: "Unknown"}
	}
	processes, _ := os.ReadDir(root)
	for _, entry := range processes {
		pid, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil || pid <= 0 {
			continue
		}
		processRoot := filepath.Join(root, entry.Name())
		fds, _ := os.ReadDir(filepath.Join(processRoot, "fd"))
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(processRoot, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			info, ok := byInode[inode]
			if !ok || info.PID != 0 {
				continue
			}
			info.PID = int32(pid)
			if name, err := os.ReadFile(filepath.Join(processRoot, "comm")); err == nil {
				info.Name = strings.TrimSpace(string(name))
			}
			byInode[inode] = info
		}
	}
	byPort := make(map[uint32]PortInfo)
	for _, item := range listeners {
		info := byInode[item.inode]
		if previous, ok := byPort[item.port]; !ok || previous.PID == 0 {
			byPort[item.port] = info
		}
	}
	result := make([]PortInfo, 0, len(byPort))
	for _, info := range byPort {
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Port < result[j].Port })
	return result, nil
}
