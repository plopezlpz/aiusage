//go:build !unix

package main

import (
	"errors"
	"os"
)

func openPrivateClaudeCredentials(string) (*os.File, error) {
	return nil, errors.New("secure Claude credential-file reads are unsupported on this platform")
}
