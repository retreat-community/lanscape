package catalog

import (
	"fmt"
	"strings"

	"github.com/retreat-community/lanscape/internal/discovery"
)

// Change kinds in the change feed (§8.3).
const (
	ChangeServiceNew       = "service_new"
	ChangePortOpened       = "port_opened"
	ChangePortClosed       = "port_closed"
	ChangeContainerGone    = "container_gone"
	ChangeContainerStopped = "container_stopped"
	ChangeContainerStarted = "container_started"
	ChangeIPChanged        = "ip_changed"
	ChangeObjectNew        = "object_new"
	ChangeObjectGone       = "object_gone"
	ChangeDeviceNew        = "device_new"
	ChangeMACNew           = "mac_new"
)

// Change is a detected difference between two reports of the same source.
type Change struct {
	Kind    string
	Subject string
	Detail  string
}

// Diff compares the previous and current items of one agent source. host names the agent.
func Diff(prev, cur []discovery.Item, host string) []Change {
	old := map[string]*discovery.Item{}
	for i := range prev {
		old[prev[i].Key] = &prev[i]
	}
	now := map[string]bool{}
	var out []Change
	for i := range cur {
		it := &cur[i]
		now[it.Key] = true
		o, existed := old[it.Key]
		switch it.Kind {
		case discovery.KindSocket:
			if !existed && include(&Finding{Item: *it}) {
				out = append(out, Change{ChangePortOpened, fmt.Sprintf("%s %s/%d", host, it.Proto, it.Port), sockDetail(it)})
			}
		case discovery.KindContainer:
			switch {
			case !existed:
				out = append(out, Change{ChangeObjectNew, host + " " + it.Name, it.Image})
			case o.State == "running" && it.State != "running":
				out = append(out, Change{ChangeContainerStopped, host + " " + it.Name, it.State})
			case o.State != "running" && it.State == "running":
				out = append(out, Change{ChangeContainerStarted, host + " " + it.Name, it.Image})
			}
			if existed && it.State == "running" && o.State == "running" && len(o.IPs) > 0 && len(it.IPs) > 0 &&
				strings.Join(o.IPs, ",") != strings.Join(it.IPs, ",") {
				out = append(out, Change{ChangeIPChanged, host + " " + it.Name, strings.Join(o.IPs, ", ") + " → " + strings.Join(it.IPs, ", ")})
			}
		case discovery.KindPVC:
		case discovery.KindDevice:
			if !existed {
				out = append(out, Change{ChangeDeviceNew, it.Name, strings.Join(it.IPs, ", ") + " " + it.Labels["type"]})
			}
		default:
			if !existed {
				out = append(out, Change{ChangeObjectNew, objName(it), it.Image})
			} else if it.Kind == discovery.KindService && len(o.IPs) > 1 && len(it.IPs) > 1 && o.IPs[1] != it.IPs[1] {
				out = append(out, Change{ChangeIPChanged, objName(it), o.IPs[1] + " → " + it.IPs[1]})
			}
		}
	}
	for i := range prev {
		it := &prev[i]
		if now[it.Key] {
			continue
		}
		switch it.Kind {
		case discovery.KindSocket:
			if include(&Finding{Item: *it}) {
				out = append(out, Change{ChangePortClosed, fmt.Sprintf("%s %s/%d", host, it.Proto, it.Port), sockDetail(it)})
			}
		case discovery.KindContainer:
			out = append(out, Change{ChangeContainerGone, host + " " + it.Name, it.Image})
		case discovery.KindPVC, discovery.KindDevice:
		default:
			out = append(out, Change{ChangeObjectGone, objName(it), ""})
		}
	}
	return out
}

func sockDetail(it *discovery.Item) string {
	if it.Process != "" {
		return it.Process
	}
	return it.Addr
}

func objName(it *discovery.Item) string {
	return strings.TrimPrefix(it.Kind, "k8s_") + " " + it.Namespace + "/" + it.Name
}
