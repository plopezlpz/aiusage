//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const providerCacheLockRetry = 10 * time.Millisecond

func lockProviderCache(name, provider string, timeout time.Duration) (func(), error) {
	path, err := cachePath()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, errors.New("timed out waiting for " + provider + " cache lock")
		}
		time.Sleep(providerCacheLockRetry)
	}
}
