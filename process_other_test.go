//go:build !unix

package main

import "testing"

func assertTestProcessExited(t *testing.T, _ int) {
	t.Helper()
}
