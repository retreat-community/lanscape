package discovery

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func fakeMDNS(t *testing.T) *net.UDPAddr {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	name := func(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }
	hdr := func(n string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: name(n), Type: typ, Class: dnsmessage.ClassINET, TTL: 120}
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) == 0 {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
			b.EnableCompression()
			_ = b.StartAnswers()
			switch q.Questions[0].Name.String() {
			case "_services._dns-sd._udp.local.":
				_ = b.PTRResource(hdr("_services._dns-sd._udp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: name("_ipp._tcp.local.")})
			case "_ipp._tcp.local.":
				_ = b.PTRResource(hdr("_ipp._tcp.local.", dnsmessage.TypePTR), dnsmessage.PTRResource{PTR: name(`Office\ Printer._ipp._tcp.local.`)})
				_ = b.StartAdditionals()
				_ = b.SRVResource(hdr(`Office\ Printer._ipp._tcp.local.`, dnsmessage.TypeSRV),
					dnsmessage.SRVResource{Target: name("printer.local."), Port: 631})
				_ = b.TXTResource(hdr(`Office\ Printer._ipp._tcp.local.`, dnsmessage.TypeTXT),
					dnsmessage.TXTResource{TXT: []string{"ty=HP LaserJet M404", "rp=ipp/print"}})
				_ = b.AResource(hdr("printer.local.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{192, 168, 1, 50}})
			default:
				continue
			}
			out, _ := b.Finish()
			_, _ = pc.WriteToUDP(out, from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func TestMDNS(t *testing.T) {
	old := mdnsAddr
	mdnsAddr = fakeMDNS(t)
	defer func() { mdnsAddr = old }()
	items, err := MDNS(context.Background(), 700*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
	p := items[0]
	if p.Key != "mdns/printer.local" || p.Name != "Office Printer" || p.Labels["type"] != "printer" ||
		p.Labels["model"] != "HP LaserJet M404" || !strings.Contains(strings.Join(p.IPs, ","), "192.168.1.50") ||
		len(p.Ports) != 1 || p.Ports[0].Port != 631 {
		t.Errorf("printer: %+v", p)
	}
}

func TestSSDP(t *testing.T) {
	desc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
			<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType><friendlyName>FRITZ!Box 7590</friendlyName>
			<manufacturer>AVM</manufacturer><modelName>FRITZ!Box 7590</modelName><presentationURL>http://192.168.178.1/</presentationURL>
			</device></root>`)
	}))
	defer desc.Close()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if strings.HasPrefix(string(buf[:n]), "M-SEARCH") {
				_, _ = pc.WriteToUDP([]byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: "+desc.URL+"/desc.xml\r\n"+
					"SERVER: Linux UPnP/1.0 AVM FRITZ!Box\r\nST: upnp:rootdevice\r\n\r\n"), from)
			}
		}
	}()
	old := ssdpAddr
	ssdpAddr = pc.LocalAddr().(*net.UDPAddr)
	defer func() { ssdpAddr = old }()
	items, err := SSDP(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
	r := items[0]
	if r.Name != "FRITZ!Box 7590" || r.Labels["manufacturer"] != "AVM" || r.Labels["type"] != "router" || r.URL != "http://192.168.178.1/" {
		t.Errorf("router: %+v", r)
	}
}

func TestDeviceType(t *testing.T) {
	cases := map[string][]string{"printer": {"_ipp._tcp"}, "media": {"_googlecast._tcp"}, "iot": {"_hap._tcp"},
		"nas": {"_smb._tcp", "_http._tcp"}, "device": {"_http._tcp"}}
	for want, svcs := range cases {
		if got := DeviceType(svcs, ""); got != want {
			t.Errorf("%v: %s, want %s", svcs, got, want)
		}
	}
}
