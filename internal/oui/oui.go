// Package oui maps MAC address prefixes to vendors (IEEE MA-L registry).
package oui

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

//go:generate go run gen.go

//go:embed oui.txt.gz
var data []byte

var (
	once  sync.Once
	table map[string]string
)

func load() {
	table = map[string]string{}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			table[k] = v
		}
	}
}

// Vendor returns the organisation for a MAC address ("" when unknown or locally administered).
func Vendor(mac string) string {
	h := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
	if len(h) < 6 {
		return ""
	}
	// locally administered (random / virtual) addresses have no vendor
	if b := h[1]; b == '2' || b == '6' || b == 'A' || b == 'E' {
		return ""
	}
	once.Do(load)
	return table[h[:6]]
}
