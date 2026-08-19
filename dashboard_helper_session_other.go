//go:build !unix

package main

import "os"

const dashboardHelperSessionMarker = "AIUSAGE_MACOS_HOST_HELPER"

func isolateDashboardHelper() error {
	if _, marked := os.LookupEnv(dashboardHelperSessionMarker); marked {
		_ = os.Unsetenv(dashboardHelperSessionMarker)
	}
	return nil
}
