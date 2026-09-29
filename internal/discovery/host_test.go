//go:build !lanscape_small

package discovery

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

func TestNUT(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.TrimSpace(l) {
			case "LIST UPS":
				_, _ = c.Write([]byte("BEGIN LIST UPS\nUPS eaton \"Eaton 5E \\\"rack\\\"\"\nEND LIST UPS\n"))
			case "LIST VAR eaton":
				_, _ = c.Write([]byte("BEGIN LIST VAR eaton\nVAR eaton battery.charge \"87\"\nVAR eaton ups.load \"23\"\n" +
					"VAR eaton ups.status \"OB DISCHRG\"\nVAR eaton battery.runtime \"1260\"\nVAR eaton device.mfr \"EATON\"\nEND LIST VAR eaton\n"))
			}
		}
	}()
	items, err := NUT(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != "on_battery" || items[0].Labels["charge"] != "87" || items[0].Labels["runtime_s"] != "1260" ||
		items[0].Labels["description"] != `Eaton 5E "rack"` {
		t.Errorf("nut: %+v", items)
	}
}

func TestSmartAndZpool(t *testing.T) {
	d, ok := ParseSmart([]byte(`{"device":{"name":"/dev/sda","type":"sat"},"model_name":"WDC WD40EFRX","serial_number":"WD-1",
		"user_capacity":{"bytes":4000787030016},"smart_status":{"passed":true},"temperature":{"current":34},"power_on_time":{"hours":31000},
		"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":8}},{"id":197,"raw":{"value":0}}]}}`))
	if !ok || d.State != "warning" || d.Labels["reallocated"] != "8" || d.Labels["temp_c"] != "34" || d.Key != "disk/WD-1" {
		t.Errorf("smart: %+v", d)
	}
	n, ok := ParseSmart([]byte(`{"device":{"name":"/dev/nvme0","type":"nvme"},"smart_status":{"passed":false},
		"nvme_smart_health_information_log":{"percentage_used":3}}`))
	if !ok || n.State != "failing" {
		t.Errorf("nvme: %+v", n)
	}
	pools := ParseZpool("tank\t7999999999999\t5000000000000\t2999999999999\tONLINE\nbackup\t100\t50\t50\tDEGRADED\n")
	if len(pools) != 2 || pools[1].State != "degraded" || pools[0].Labels["alloc"] != "5000000000000" {
		t.Errorf("zpool: %+v", pools)
	}
}
