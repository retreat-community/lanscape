package agent

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"time"
)

// LLDPPeer is a switch or host seen on a link through LLDP or CDP (via lldpd).
type LLDPPeer struct {
	Iface     string `json:"iface"`
	Proto     string `json:"proto"` // LLDP, CDPv2 …
	Name      string `json:"name"`  // system name
	ChassisID string `json:"chassis_id,omitempty"`
	MgmtIP    string `json:"mgmt_ip,omitempty"`
	Descr     string `json:"descr,omitempty"`
	Port      string `json:"port"`
	PortDescr string `json:"port_descr,omitempty"`
}

// collectLLDP asks lldpd for its neighbours when it is installed.
func collectLLDP(ctx context.Context) []LLDPPeer {
	if _, err := exec.LookPath("lldpcli"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "lldpcli", "-f", "json0", "show", "neighbors", "details").Output()
	if err != nil {
		return nil
	}
	return ParseLLDP(out)
}

// ParseLLDP reads "lldpcli -f json0 show neighbors" (the stable list-based JSON format).
func ParseLLDP(b []byte) []LLDPPeer {
	type val struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	}
	var doc struct {
		LLDP []struct {
			Interface []struct {
				Name    string `json:"name"`
				Via     string `json:"via"`
				Chassis []struct {
					ID     []val `json:"id"`
					Name   []val `json:"name"`
					Descr  []val `json:"descr"`
					MgmtIP []val `json:"mgmt-ip"`
				} `json:"chassis"`
				Port []struct {
					ID    []val `json:"id"`
					Descr []val `json:"descr"`
				} `json:"port"`
			} `json:"interface"`
		} `json:"lldp"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	first := func(v []val) string {
		if len(v) > 0 {
			return v[0].Value
		}
		return ""
	}
	var out []LLDPPeer
	for _, l := range doc.LLDP {
		for _, ifc := range l.Interface {
			p := LLDPPeer{Iface: ifc.Name, Proto: ifc.Via}
			if len(ifc.Chassis) > 0 {
				c := ifc.Chassis[0]
				p.ChassisID, p.Name, p.Descr, p.MgmtIP = first(c.ID), first(c.Name), first(c.Descr), first(c.MgmtIP)
			}
			if len(ifc.Port) > 0 {
				p.Port, p.PortDescr = first(ifc.Port[0].ID), first(ifc.Port[0].Descr)
			}
			if p.Name == "" {
				p.Name = p.ChassisID
			}
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Iface < out[j].Iface })
	return out
}
