//go:build !unix && !windows

package main

import (
	"errors"
	"time"
)

func lockProviderCache(_, provider string, _ time.Duration) (func(), error) {
	return nil, errors.New("cross-process " + provider + " cache locking is unsupported on this platform")
}
