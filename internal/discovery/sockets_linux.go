package discovery

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sockets lists listening TCP and UDP sockets of the agent's network namespace with
// their processes (requires root or CAP_SYS_PTRACE to see other users' processes).
func Sockets() ([]Item, error) {
	var all []Listener
	for _, t := range []struct {
		file  string
		proto string
		v6    bool
	}{{"tcp", "tcp", false}, {"tcp6", "tcp", true}, {"udp", "udp", false}, {"udp6", "udp", true}} {
		f, err := os.Open(filepath.Join("/proc/net", t.file))
		if err != nil {
			continue
		}
		ls, err := ParseProcNet(f, t.proto, t.v6)
		f.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, ls...)
	}
	owners := socketOwners()
	users := map[int]string{}
	if b, err := os.ReadFile("/etc/passwd"); err == nil {
		users = ParsePasswd(string(b))
	}
	return mergeListeners(all, func(inode uint64) (int, string, string) {
		o, ok := owners[inode]
		if !ok {
			return 0, "", ""
		}
		return o.pid, o.name, o.container
	}, users), nil
}

type owner struct {
	pid       int
	name      string
	container string
}

// socketOwners maps socket inodes to processes by scanning /proc/*/fd.
func socketOwners() map[uint64]owner {
	m := map[uint64]owner{}
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", p.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		var o *owner
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
			if err != nil {
				continue
			}
			if _, ok := m[inode]; ok {
				continue
			}
			if o == nil {
				o = &owner{pid: pid}
				if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
					o.name = strings.TrimSpace(string(b))
				}
				if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
					o.container = ContainerFromCgroup(string(b))
				}
			}
			m[inode] = *o
		}
	}
	return m
}
