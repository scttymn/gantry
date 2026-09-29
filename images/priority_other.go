//go:build !linux

package images

// lowerPriority is Linux's alone: servers run there.
func lowerPriority() {}
