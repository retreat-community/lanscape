//go:build lanscape_small

package discovery

import "encoding/json"

// small builds (OpenWrt) read kubeconfig files in JSON form only ("kubectl config view --raw -o json")
func unmarshalKubeconfig(b []byte, v *kubeconfig) error { return json.Unmarshal(b, v) }
