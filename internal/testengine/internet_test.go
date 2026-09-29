package testengine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInternet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/trace":
			_, _ = io.WriteString(w, "fl=123\nh=1.1.1.1\nip=203.0.113.7\nts=1\n")
		case "/plain":
			_, _ = io.WriteString(w, "2001:db8::7\n")
		case "/down":
			chunk := bytes.Repeat([]byte{0}, 64<<10)
			for i := 0; i < 400; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := Internet(context.Background(), nil, InternetParams{IPURL: srv.URL + "/trace", DownloadURL: srv.URL + "/down", Duration: 2 * time.Second})
	if len(r) != 1 || !r[0].OK || r[0].PublicIP != "203.0.113.7" || r[0].DownMbps <= 0 || r[0].Error != "" {
		t.Fatalf("%+v", r)
	}
	r = Internet(context.Background(), nil, InternetParams{IPURL: srv.URL + "/plain"})
	if !r[0].OK || r[0].PublicIP != "2001:db8::7" || r[0].DownMbps != 0 {
		t.Errorf("plain: %+v", r)
	}
	r = Internet(context.Background(), []Gateway{{Dev: "", Gateway: "10.0.0.1"}}, InternetParams{IPURL: srv.URL + "/missing"})
	if r[0].OK || r[0].Error == "" || r[0].Gateway != "10.0.0.1" {
		t.Errorf("error: %+v", r)
	}
}
