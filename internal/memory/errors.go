package memory

import "errors"

var (
	// ErrNotFound is returned when a requested memory entry does not exist.
	ErrNotFound = errors.New("memory: not found")
	// ErrDisabled is returned when the memory subsystem is turned off.
	ErrDisabled = errors.New("memory: disabled")
)
