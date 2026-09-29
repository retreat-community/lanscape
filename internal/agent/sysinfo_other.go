//go:build !linux

package agent

import "os"

// CollectSystem returns what the portable runtime knows on non-Linux systems.
func CollectSystem() (routes []Route, rules []string, neigh []Neighbor, env Env, res Resources) {
	res = baseResources()
	env = DetectEnv("", os.Getenv)
	return nil, nil, nil, env, res
}
