package main

import (
	"bytes"
	"strings"
	"testing"
)

// The token flag's default is printed by --help and by every usage error. It
// must not be the token.
func TestHelpDoesNotPrintTheToken(t *testing.T) {
	t.Setenv("FIGMA_TOKEN", "not-a-real-token-keep-me-out-of-help")
	for _, args := range [][]string{{"--help"}, {}} { // --help, and a usage error (no --url)
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		_ = cmd.Execute()
		if strings.Contains(out.String(), "not-a-real-token") {
			t.Errorf("args %v printed the token:\n%s", args, out.String())
		}
		if !strings.Contains(out.String(), "FIGMA_TOKEN") {
			t.Errorf("args %v: the usage does not say the token can come from FIGMA_TOKEN:\n%s", args, out.String())
		}
	}
}
