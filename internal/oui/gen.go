//go:build ignore

// gen downloads the IEEE MA-L registry and writes oui.txt.gz ("AABBCC<TAB>Organization" per
// line, sorted). Run "go generate ./internal/oui" to refresh it; it needs network access.
package main

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

func main() {
	req, _ := http.NewRequest(http.MethodGet, "https://standards-oui.ieee.org/oui/oui.csv", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; lanscape-oui-gen)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("oui.csv: %s", resp.Status)
	}
	r := csv.NewReader(resp.Body)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		log.Fatal(err)
	}
	if len(rows) < 1000 {
		log.Fatalf("oui.csv: only %d rows", len(rows))
	}
	m := map[string]string{}
	for _, row := range rows[1:] {
		if len(row) < 3 || len(row[1]) != 6 {
			continue
		}
		m[strings.ToUpper(row[1])] = strings.Join(strings.Fields(row[2]), " ")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.Create("oui.txt.gz")
	if err != nil {
		log.Fatal(err)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	for _, k := range keys {
		fmt.Fprintf(zw, "%s\t%s\n", k, m[k])
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}
