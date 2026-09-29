//go:build !lanscape_small

package fingerprint

import "gopkg.in/yaml.v3"

func unmarshalYAML(data []byte, v any) error { return yaml.Unmarshal(data, v) }
