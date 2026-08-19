//go:build unix

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const dashboardHelperSessionMarker = "AIUSAGE_MACOS_HOST_HELPER"

func isolateDashboardHelper() error {
	if _, marked := os.LookupEnv(dashboardHelperSessionMarker); !marked {
		return nil
	}
	_ = os.Unsetenv(dashboardHelperSessionMarker)

	pid := os.Getpid()
	session, err := unix.Getsid(pid)
	if err != nil {
		return fmt.Errorf("isolate dashboard helper: inspect session: %w", err)
	}
	if session == pid {
		return nil
	}
	if _, err := unix.Setsid(); err != nil {
		if session, checkErr := unix.Getsid(pid); checkErr == nil && session == pid {
			return nil
		}
		return fmt.Errorf("isolate dashboard helper: create session: %w", err)
	}
	if session, err = unix.Getsid(pid); err != nil || session != pid {
		if err != nil {
			return fmt.Errorf("isolate dashboard helper: verify session: %w", err)
		}
		return fmt.Errorf("isolate dashboard helper: process %d is in session %d", pid, session)
	}
	return nil
}
