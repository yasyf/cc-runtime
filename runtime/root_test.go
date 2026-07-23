package runtime

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/version"
)

func TestVersionCommand(t *testing.T) {
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("version output = %q, want %q", got, version.Version)
	}
}

// TestVersionFlag proves `cc-runtime --version` prints the bare version the mesh
// verify parses off a peer over ssh.
func TestVersionFlag(t *testing.T) {
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != version.Version {
		t.Fatalf("--version output = %q, want the bare version %q", got, version.Version)
	}
}

func TestDaemonStopControlCommandHidden(t *testing.T) {
	command, _, err := Root().Find([]string{daemon.StopControlCommand})
	if err != nil {
		t.Fatalf("find stop control command: %v", err)
	}
	if !command.Hidden {
		t.Fatal("daemon stop control command must stay hidden")
	}
}
