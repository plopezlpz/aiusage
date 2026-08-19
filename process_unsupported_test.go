//go:build !unix

package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestUnsupportedPlatformsDoNotStartCollectors(t *testing.T) {
	codexCalled := false
	_, _, codexErr := fetchCodex(context.Background(), func(context.Context) *exec.Cmd {
		codexCalled = true
		return exec.Command("codex")
	})
	kimiCalled := false
	_, kimiErr := fetchKimi(context.Background(), func(context.Context) *exec.Cmd {
		kimiCalled = true
		return exec.Command("kimi")
	}, "unused", time.Now())
	if codexCalled || codexErr == nil || !strings.Contains(codexErr.Error(), "Unix") {
		t.Fatalf("Codex unsupported behavior: called=%v err=%v", codexCalled, codexErr)
	}
	if kimiCalled || kimiErr == nil || !strings.Contains(kimiErr.Error(), "process-group") {
		t.Fatalf("Kimi unsupported behavior: called=%v err=%v", kimiCalled, kimiErr)
	}
}
