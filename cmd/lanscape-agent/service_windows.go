//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/retreat-community/lanscape/internal/agent"
)

const serviceName = "LanscapeAgent"

func defaultDataDir() string {
	return filepath.Join(os.Getenv("ProgramData"), "Lanscape", "agent")
}

type winService struct{ a *agent.Agent }

func (w *winService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- w.a.Run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		case <-done:
			return false, 1
		}
	}
}

func runService(ctx context.Context, a *agent.Agent) error {
	if is, err := svc.IsWindowsService(); err == nil && is {
		return svc.Run(serviceName, &winService{a: a})
	}
	return a.Run(ctx)
}

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lanscape-agent service install|uninstall [agent flags]")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		s, err := m.CreateService(serviceName, exe, mgr.Config{DisplayName: "Lanscape Agent", StartType: mgr.StartAutomatic,
			Description: "Lanscape network tests, discovery and checks"}, append([]string{"run"}, args[1:]...)...)
		if err != nil {
			return err
		}
		defer s.Close()
		return s.Start()
	case "uninstall":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return err
		}
		defer s.Close()
		_, _ = s.Control(svc.Stop)
		return s.Delete()
	}
	return fmt.Errorf("unknown service command %s", strings.Join(args, " "))
}
