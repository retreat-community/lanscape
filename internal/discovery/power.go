//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

var ipmiWatts = regexp.MustCompile(`(?i)instantaneous power reading:\s*(\d+)\s*watts`)

// ParseIPMIPower reads "ipmitool dcmi power reading" output.
func ParseIPMIPower(out string) (int, bool) {
	m := ipmiWatts.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	w, err := strconv.Atoi(m[1])
	return w, err == nil
}

func ipmiPower(ctx context.Context) []Item {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ipmitool", "dcmi", "power", "reading").Output()
	if err != nil {
		return nil
	}
	w, ok := ParseIPMIPower(string(out))
	if !ok {
		return nil
	}
	return []Item{{Key: "power/ipmi", Kind: KindPower, Name: "BMC", State: "on",
		Labels: map[string]string{"watts": strconv.Itoa(w), "source": "ipmi"}}}
}

// plugPower asks a plug in the dialects of Shelly Gen2+, Shelly Gen1 and Tasmota.
func plugPower(ctx context.Context, hc *http.Client, p Plug) Item {
	it := Item{Key: "power/" + p.Name, Kind: KindPower, Name: p.Name, URL: p.URL, State: "unknown", Labels: map[string]string{}}
	get := func(path string, v any) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+path, nil)
		if err != nil {
			return false
		}
		res, err := hc.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			return false
		}
		return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v) == nil
	}
	set := func(src string, watts float64, on bool) Item {
		it.Labels["source"], it.Labels["watts"] = src, strconv.FormatFloat(watts, 'f', 1, 64)
		it.State = "off"
		if on {
			it.State = "on"
		}
		return it
	}
	var g2 struct {
		APower *float64 `json:"apower"`
		Output bool     `json:"output"`
	}
	if get("/rpc/Switch.GetStatus?id=0", &g2) && g2.APower != nil {
		return set("shelly", *g2.APower, g2.Output)
	}
	var g1 struct {
		Meters []struct {
			Power float64 `json:"power"`
		} `json:"meters"`
		Relays []struct {
			IsOn bool `json:"ison"`
		} `json:"relays"`
	}
	if get("/status", &g1) && len(g1.Meters) > 0 {
		return set("shelly", g1.Meters[0].Power, len(g1.Relays) == 0 || g1.Relays[0].IsOn)
	}
	var tas struct {
		StatusSNS struct {
			Energy *struct {
				Power json.Number `json:"Power"`
			} `json:"ENERGY"`
		} `json:"StatusSNS"`
	}
	if get("/cm?cmnd=Status%208", &tas) && tas.StatusSNS.Energy != nil {
		w, _ := tas.StatusSNS.Energy.Power.Float64()
		return set("tasmota", w, w > 0)
	}
	it.Labels["error"] = fmt.Sprintf("%s: no Shelly or Tasmota power reading", p.URL)
	return it
}
