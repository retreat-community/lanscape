package agent

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Pending package updates are counted from the local package index (no refresh, no network)
// every few hours in the background; inventories carry the last count.
var updates struct {
	once  sync.Once
	mu    sync.Mutex
	count int
}

const updatesEvery = 6 * time.Hour

// pendingUpdates returns the last count (0 until the first check finished) and starts the
// background checker on first use.
func pendingUpdates() int {
	updates.once.Do(func() {
		go func() {
			for {
				n := countUpdates(context.Background())
				updates.mu.Lock()
				updates.count = n
				updates.mu.Unlock()
				time.Sleep(updatesEvery)
			}
		}()
	})
	updates.mu.Lock()
	defer updates.mu.Unlock()
	return updates.count
}

// updateCommand is one package manager: the command and how to count its output.
type updateCommand struct {
	bin   string
	args  []string
	count func(out string) int
}

var updateCommands = []updateCommand{
	// Debian, Ubuntu, Proxmox: simulate an upgrade from the cached index
	{"apt-get", []string{"-s", "-o", "Debug::NoLocking=1", "upgrade"}, countPrefix("Inst ")},
	// Fedora, RHEL: exit status 100 means updates are listed
	{"dnf", []string{"-q", "--cacheonly", "check-update"}, countDNF},
	// Alpine
	{"apk", []string{"version", "-l", "<"}, countAfterHeader},
	// OpenWrt
	{"opkg", []string{"list-upgradable"}, countLines},
	// FreeBSD
	{"pkg", []string{"version", "-vRL="}, countLines},
}

func countUpdates(ctx context.Context) int {
	for _, c := range updateCommands {
		if _, err := exec.LookPath(c.bin); err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		out, err := exec.CommandContext(cctx, c.bin, c.args...).Output() //nolint:gosec // fixed commands
		cancel()
		if err != nil && len(out) == 0 {
			continue
		}
		return c.count(string(out))
	}
	return 0
}

func countPrefix(prefix string) func(string) int {
	return func(out string) int {
		n := 0
		sc := bufio.NewScanner(strings.NewReader(out))
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), prefix) {
				n++
			}
		}
		return n
	}
}

func countLines(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// countAfterHeader skips "Installed: Available:" style header lines.
func countAfterHeader(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "Installed:") && !strings.HasPrefix(l, "WARNING") {
			n++
		}
	}
	return n
}

// countDNF counts package lines ("name.arch  version  repo"), not the obsoletes section.
func countDNF(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Obsoleting") {
			break
		}
		if f := strings.Fields(l); len(f) == 3 && strings.Contains(f[0], ".") {
			n++
		}
	}
	return n
}
