//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDashboardHelperSessionIsolation(t *testing.T) {
	if os.Getenv("AIUSAGE_TEST_SESSION_HELPER") == "1" {
		if err := isolateDashboardHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.Setenv(dashboardHelperSessionMarker, "1"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := isolateDashboardHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		session, err := unix.Getsid(os.Getpid())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_, marked := os.LookupEnv(dashboardHelperSessionMarker)
		fmt.Printf("SESSION %d %d %t\n", os.Getpid(), session, marked)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDashboardHelperSessionIsolation$")
	cmd.Env = append(os.Environ(), "AIUSAGE_TEST_SESSION_HELPER=1", dashboardHelperSessionMarker+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated helper failed: %v: %s", err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 4 || fields[0] != "SESSION" {
		t.Fatalf("unexpected helper output: %q", output)
	}
	pid, pidErr := strconv.Atoi(fields[1])
	session, sessionErr := strconv.Atoi(fields[2])
	if pidErr != nil || sessionErr != nil || pid != session || fields[3] != "false" {
		t.Fatalf("helper was not an unmarked session leader: %q", output)
	}
}

func TestDashboardHelperProcessGroupLeaderFailsClosed(t *testing.T) {
	if os.Getenv("AIUSAGE_TEST_GROUP_LEADER_HELPER") == "1" {
		err := isolateDashboardHelper()
		_, marked := os.LookupEnv(dashboardHelperSessionMarker)
		if err == nil || marked {
			fmt.Fprintf(os.Stderr, "expected isolation failure with marker removed: error=%v marked=%t\n", err, marked)
			os.Exit(2)
		}
		fmt.Println("FAILED_CLOSED")
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDashboardHelperProcessGroupLeaderFailsClosed$")
	cmd.Env = append(os.Environ(), "AIUSAGE_TEST_GROUP_LEADER_HELPER=1", dashboardHelperSessionMarker+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("process-group leader check failed: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "FAILED_CLOSED" {
		t.Fatalf("unexpected process-group leader output: %q", output)
	}
}

func TestDashboardHelperWithoutMarkerKeepsSession(t *testing.T) {
	t.Setenv(dashboardHelperSessionMarker, "")
	if err := os.Unsetenv(dashboardHelperSessionMarker); err != nil {
		t.Fatal(err)
	}
	before, err := unix.Getsid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := isolateDashboardHelper(); err != nil {
		t.Fatal(err)
	}
	after, err := unix.Getsid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unmarked process session changed from %d to %d", before, after)
	}
}
