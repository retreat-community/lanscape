//go:build !linux

package agent

import "context"

// watchChanges is not available; the periodic inventory covers changes.
func watchChanges(context.Context) <-chan struct{} { return make(chan struct{}) }
