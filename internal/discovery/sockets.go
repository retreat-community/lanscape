package discovery

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Listener is a listening socket parsed from /proc/net/{tcp,udp}[6].
type Listener struct {
	Proto string // tcp, udp
	Addr  string
	Port  int
	UID   int
	Inode uint64
}

// ParseProcNet parses a /proc/net/tcp|udp[6] table and returns listening sockets
// (TCP state LISTEN, unconnected UDP sockets).
func ParseProcNet(r io.Reader, proto string, v6 bool) ([]Listener, error) {
	var out []Listener
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		st := f[3]
		if (proto == "tcp" && st != "0A") || (proto == "udp" && st != "07") {
			continue
		}
		if proto == "udp" && !strings.HasSuffix(f[2], ":0000") {
			continue
		}
		ip, port, err := parseHexAddr(f[1], v6)
		if err != nil {
			return nil, err
		}
		uid, _ := strconv.Atoi(f[7])
		inode, _ := strconv.ParseUint(f[9], 10, 64)
		out = append(out, Listener{Proto: proto, Addr: ip.String(), Port: port, UID: uid, Inode: inode})
	}
	return out, sc.Err()
}

func parseHexAddr(s string, v6 bool) (net.IP, int, error) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	b, err := hex.DecodeString(s[:i])
	if err != nil || (v6 && len(b) != 16) || (!v6 && len(b) != 4) {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	// the kernel prints each 32-bit word in host (little-endian) order
	for w := 0; w < len(b); w += 4 {
		b[w], b[w+1], b[w+2], b[w+3] = b[w+3], b[w+2], b[w+1], b[w]
	}
	port, err := strconv.ParseUint(s[i+1:], 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("bad port %q", s)
	}
	return net.IP(b), int(port), nil
}

// ContainerFromCgroup extracts a container id from /proc/<pid>/cgroup content
// (docker-<id>.scope, /docker/<id>, containerd cri-containerd-<id>.scope).
func ContainerFromCgroup(s string) string {
	for _, l := range strings.Split(s, "\n") {
		for _, pfx := range []string{"docker-", "/docker/", "cri-containerd-", "crio-", "libpod-"} {
			i := strings.Index(l, pfx)
			if i < 0 {
				continue
			}
			id := l[i+len(pfx):]
			id = strings.TrimSuffix(id, ".scope")
			if j := strings.IndexAny(id, "/."); j >= 0 {
				id = id[:j]
			}
			if len(id) >= 12 && isHex(id) {
				return id[:12]
			}
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ParsePasswd maps uid to user name.
func ParsePasswd(s string) map[int]string {
	m := map[int]string{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.Split(l, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			m[uid] = f[0]
		}
	}
	return m
}

func wildcard(a string) bool { return a == "0.0.0.0" || a == "::" || a == "" }

// mergeListeners folds IPv4/IPv6 wildcard listeners of the same process and port into one item.
func mergeListeners(ls []Listener, proc func(inode uint64) (pid int, name, container string), users map[int]string) []Item {
	byKey := map[string]*Item{}
	var order []string
	for _, l := range ls {
		pid, name, cont := 0, "", ""
		if proc != nil {
			pid, name, cont = proc(l.Inode)
		}
		addr := l.Addr
		if wildcard(addr) {
			addr = "*"
		}
		key := fmt.Sprintf("%s/%d@%s", l.Proto, l.Port, addr)
		it, ok := byKey[key]
		if !ok {
			it = &Item{Key: key, Kind: KindSocket, Proto: l.Proto, Addr: l.Addr, Port: l.Port, PID: pid, Process: name,
				User: users[l.UID], Owner: cont}
			it.Name = name
			if it.Name == "" {
				it.Name = fmt.Sprintf("%s/%d", l.Proto, l.Port)
			}
			if wildcard(l.Addr) {
				it.Addr = "0.0.0.0"
			}
			byKey[key] = it
			order = append(order, key)
		}
		if it.Process == "" && name != "" {
			it.Process, it.Name, it.PID = name, name, pid
		}
		if it.Owner == "" {
			it.Owner = cont
		}
	}
	out := make([]Item, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sortItems(out)
	return out
}
