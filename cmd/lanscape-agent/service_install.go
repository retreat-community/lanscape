//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.lanscape.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
%s  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/usr/local/var/log/lanscape-agent.log</string>
</dict>
</plist>
`

// installUnixService installs a launchd job on macOS; Linux and FreeBSD use packages.
func installUnixService(args []string) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return fmt.Errorf("usage: lanscape-agent service install|uninstall [agent flags]")
	}
	if runtime.GOOS != "darwin" {
		return errNoService
	}
	const plist = "/Library/LaunchDaemons/org.lanscape.agent.plist"
	if args[0] == "uninstall" {
		_ = exec.Command("launchctl", "unload", plist).Run()
		return os.Remove(plist)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, a := range args[1:] {
		fmt.Fprintf(&b, "    <string>%s</string>\n", strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(a))
	}
	if err := os.MkdirAll("/usr/local/var/log", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(fmt.Sprintf(launchdPlist, exe, b.String())), 0o644); err != nil {
		return err
	}
	return exec.Command("launchctl", "load", "-w", plist).Run()
}
