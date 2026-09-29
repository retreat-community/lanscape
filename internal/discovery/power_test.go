//go:build !lanscape_small

package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseIPMIPower(t *testing.T) {
	out := `
    Instantaneous power reading:                   187 Watts
    Minimum during sampling period:                 92 Watts
`
	if w, ok := ParseIPMIPower(out); !ok || w != 187 {
		t.Errorf("got %d %v", w, ok)
	}
	if _, ok := ParseIPMIPower("Error: DCMI not supported"); ok {
		t.Error("accepted an error")
	}
}

func TestParsePlugs(t *testing.T) {
	ps := ParsePlugs("nas=http://192.168.1.50/, 192.168.1.51")
	if len(ps) != 2 || ps[0] != (Plug{"nas", "http://192.168.1.50"}) || ps[1] != (Plug{"192.168.1.51", "http://192.168.1.51"}) {
		t.Errorf("%+v", ps)
	}
}

func TestPlugPower(t *testing.T) {
	bodies := map[string]map[string]string{
		"gen2":    {"/rpc/Switch.GetStatus": `{"id":0,"output":true,"apower":41.5}`},
		"gen1":    {"/status": `{"relays":[{"ison":false}],"meters":[{"power":0}]}`},
		"tasmota": {"/cm": `{"StatusSNS":{"ENERGY":{"Power":12}}}`},
		"none":    {},
	}
	for name, paths := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, ok := paths[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(b))
		}))
		it := plugPower(context.Background(), srv.Client(), Plug{Name: name, URL: srv.URL})
		srv.Close()
		want := map[string][3]string{"gen2": {"shelly", "41.5", "on"}, "gen1": {"shelly", "0.0", "off"},
			"tasmota": {"tasmota", "12.0", "on"}, "none": {"", "", "unknown"}}[name]
		if it.Labels["source"] != want[0] || it.Labels["watts"] != want[1] || it.State != want[2] {
			t.Errorf("%s: %+v", name, it)
		}
	}
}
