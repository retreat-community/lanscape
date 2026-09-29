//go:build !lanscape_small

package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDNSRecords(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"session":{"valid":true,"sid":"s1"}}`)
	})
	mux.HandleFunc("DELETE /api/auth", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/api/config/dns/hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-FTL-SID") != "s1" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"config":{"dns":{"hosts":["192.168.1.10 nas.lan nas","192.168.1.11 printer.lan"]}}}`)
	})
	mux.HandleFunc("/control/rewrite/list", func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "pw" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `[{"domain":"nas.lan","answer":"192.168.1.10"},{"domain":"*.apps.lan","answer":"192.168.1.5"}]`)
	})
	mux.HandleFunc("/api/zones/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","response":{"zones":[{"name":"home.arpa","type":"Primary"},{"name":"0.in-addr.arpa","type":"Primary","internal":true}]}}`)
	})
	mux.HandleFunc("/api/zones/records/get", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","response":{"records":[{"name":"tv.home.arpa","type":"A","rData":{"ipAddress":"192.168.1.30"}},
			{"name":"home.arpa","type":"SOA","rData":{}}]}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	items, err := DNSRecords(context.Background(), DNSConfig{PiholeURL: srv.URL, PiholePassword: "x", AdGuardURL: srv.URL,
		AdGuardUser: "admin", AdGuardPassword: "pw", TechnitiumURL: srv.URL, TechnitiumToken: "tt"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, it := range items {
		names[it.Name] = it.IPs[0]
	}
	if len(items) != 4 || names["nas.lan"] != "192.168.1.10" || names["nas"] != "192.168.1.10" || names["tv.home.arpa"] != "192.168.1.30" {
		t.Errorf("records: %+v", items)
	}
}
