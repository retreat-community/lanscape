//go:build linux

package netio

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Interfaces lists all interfaces with kind, parent, VLAN, members and link speed.
func Interfaces() ([]Iface, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("netio: list links: %w", err)
	}
	byIndex := map[int]string{}
	for _, l := range links {
		byIndex[l.Attrs().Index] = l.Attrs().Name
	}
	vlancfg := map[string][2]string{}
	if f, err := os.Open("/proc/net/vlan/config"); err == nil {
		vlancfg = ParseVLANConfig(f)
		f.Close()
	}
	out := make([]Iface, 0, len(links))
	members := map[string][]string{}
	for _, l := range links {
		a := l.Attrs()
		i := Iface{
			Name:    a.Name,
			Index:   a.Index,
			MAC:     a.HardwareAddr.String(),
			MTU:     a.MTU,
			Up:      a.Flags&net.FlagUp != 0,
			Carrier: a.OperState == netlink.OperUp || a.RawFlags&unix.IFF_LOWER_UP != 0,
			Kind:    kind(l),
		}
		if a.ParentIndex != 0 && a.ParentIndex != a.Index {
			i.Parent = byIndex[a.ParentIndex]
		}
		if a.MasterIndex != 0 {
			i.Master = byIndex[a.MasterIndex]
			members[i.Master] = append(members[i.Master], a.Name)
		}
		switch v := l.(type) {
		case *netlink.Vlan:
			i.VLAN = v.VlanId
		case *netlink.Bond:
			i.BondMode = v.Mode.String()
		}
		if i.VLAN == 0 {
			if c, ok := vlancfg[a.Name]; ok {
				i.VLAN, _ = strconv.Atoi(c[0])
				if i.Parent == "" {
					i.Parent = c[1]
				}
			}
		}
		i.Speed = LinkSpeed(a.Name)
		addrs, err := netlink.AddrList(l, netlink.FAMILY_ALL)
		if err == nil {
			for _, ad := range addrs {
				if ad.IP.IsLinkLocalUnicast() {
					continue
				}
				ones, _ := ad.Mask.Size()
				i.Addrs = append(i.Addrs, Addr{IP: ad.IP.String(), Prefix: ones})
			}
		}
		out = append(out, i)
	}
	for k := range out {
		if m := members[out[k].Name]; len(m) > 0 {
			sort.Strings(m)
			out[k].Members = m
		}
	}
	return out, nil
}

func kind(l netlink.Link) string {
	if l.Attrs().Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	switch t := l.Type(); t {
	case "device":
		return "physical"
	case "tuntap":
		return "tun"
	default:
		return t
	}
}

type ethtoolCmd struct {
	Cmd           uint32
	Supported     uint32
	Advertising   uint32
	Speed         uint16
	Duplex        uint8
	Port          uint8
	PhyAddress    uint8
	Transceiver   uint8
	Autoneg       uint8
	MdioSupport   uint8
	Maxtxpkt      uint32
	Maxrxpkt      uint32
	SpeedHi       uint16
	EthTpMdix     uint8
	EthTpMdixCtrl uint8
	LpAdvertising uint32
	Reserved      [2]uint32
}

type ifreqData struct {
	Name [unix.IFNAMSIZ]byte
	Data uintptr
	_    [24]byte
}

const ethtoolGSet = 0x00000001

// LinkSpeed returns the negotiated speed in Mbit/s via SIOCETHTOOL, 0 when unknown.
func LinkSpeed(name string) int {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0
	}
	defer unix.Close(fd)
	cmd := ethtoolCmd{Cmd: ethtoolGSet}
	var req ifreqData
	copy(req.Name[:], name)
	req.Data = uintptr(unsafe.Pointer(&cmd))                                                                  //nolint:gosec // SIOCETHTOOL takes a pointer to struct ethtool_cmd
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&req))) //nolint:gosec // ioctl ABI
	if errno != 0 {
		return 0
	}
	return NormalizeSpeed(uint32(cmd.Speed) | uint32(cmd.SpeedHi)<<16)
}

// NormalizeSpeed maps driver "unknown" values (-1, 0, 65535) to 0.
func NormalizeSpeed(raw uint32) int {
	if raw == 0 || raw == 0xffff || raw == 0xffffffff {
		return 0
	}
	return int(raw)
}

// ReadCounters returns RX/TX byte counters of an interface.
func ReadCounters(name string) (Counters, error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		return Counters{}, fmt.Errorf("netio: %s: %w", name, err)
	}
	s := l.Attrs().Statistics
	if s == nil {
		return Counters{}, fmt.Errorf("netio: %s: no statistics", name)
	}
	return Counters{RX: s.RxBytes, TX: s.TxBytes}, nil
}

// RouteDev returns the interface the kernel would use for dst from src
// (equivalent of "ip route get <dst> from <src>"), honouring policy rules.
func RouteDev(dst, src net.IP) (string, error) {
	opts := &netlink.RouteGetOptions{}
	if src != nil {
		opts.SrcAddr = src
	}
	routes, err := netlink.RouteGetWithOptions(dst, opts)
	if err != nil {
		return "", fmt.Errorf("netio: route get %s: %w", dst, err)
	}
	if len(routes) == 0 {
		return "", errors.New("netio: no route")
	}
	l, err := netlink.LinkByIndex(routes[0].LinkIndex)
	if err != nil {
		return "", err
	}
	return l.Attrs().Name, nil
}

// BindControl returns a net.Dialer/ListenConfig Control function that binds the
// socket to dev (SO_BINDTODEVICE) and optionally sets DF for PMTU probes.
func BindControl(dev string, df bool) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if dev != "" {
				if e := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, dev); e != nil {
					serr = fmt.Errorf("netio: bind to %s: %w", dev, e)
					return
				}
			}
			if df {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_PROBE)
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// OutQueue returns the number of bytes not yet sent by the kernel for a TCP socket.
func OutQueue(c syscall.RawConn) int {
	n := 0
	_ = c.Control(func(fd uintptr) {
		n, _ = unix.IoctlGetInt(int(fd), unix.SIOCOUTQ)
	})
	return n
}
