//go:build !lanscape_small

package discovery

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

func unmarshalKubeconfig(b []byte, v *kubeconfig) error {
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		return json.Unmarshal(b, v)
	}
	return yaml.Unmarshal(b, v)
}
