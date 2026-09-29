//go:build ignore

// gen converts signatures.yaml (the editable source) into the compact signatures.json that
// is embedded into the binaries, so agents do not need a YAML parser.
package main

import (
	"encoding/json"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	src, err := os.ReadFile("signatures.yaml")
	if err != nil {
		log.Fatal(err)
	}
	var v []map[string]any
	if err := yaml.Unmarshal(src, &v); err != nil {
		log.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("signatures.json", append(out, '\n'), 0o644); err != nil { //nolint:gosec // generated source file
		log.Fatal(err)
	}
}
