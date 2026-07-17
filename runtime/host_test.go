package runtime

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/synckit/hostregistry"
)

func TestPrintHostListEmpty(t *testing.T) {
	var out bytes.Buffer
	reg := &hostregistry.Registry{Self: "alice@mac.tail.ts.net"}
	if err := printHostList(context.Background(), &out, reg, meshDial); err != nil {
		t.Fatalf("printHostList: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "self: alice@mac.tail.ts.net") {
		t.Fatalf("missing self line: %q", got)
	}
	if !strings.Contains(got, "no peers registered") {
		t.Fatalf("missing empty notice: %q", got)
	}
}

func TestPrintHostListLiveColumn(t *testing.T) {
	var out bytes.Buffer
	reg := &hostregistry.Registry{
		Self:  "alice@mac.tail.ts.net",
		Hosts: []string{"bob@srv.tail.ts.net", "dave@bare.tail.ts.net", "carol@lap.tail.ts.net"},
	}
	dial := func(target string) hostregistry.Runner {
		m := hostregistry.NewMockRunner()
		switch target {
		case "carol@lap.tail.ts.net":
			// Unreachable: every probe fails.
			return m.DefaultSSH("", errors.New("down"))
		case "dave@bare.tail.ts.net":
			// Reachable but cc-runtime not installed.
			return m.OnSSH("command -v cc-runtime", "", errors.New("exit status 1")).
				OnSSH("true", "", nil)
		default:
			return m.OnSSH("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
				OnSSH("--version", "1.4.2\n", nil)
		}
	}
	if err := printHostList(context.Background(), &out, reg, dial); err != nil {
		t.Fatalf("printHostList: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("host list = %d lines, want self + header + 3 rows:\n%s", len(lines), out.String())
	}
	if lines[0] != "self: alice@mac.tail.ts.net" {
		t.Fatalf("self line = %q", lines[0])
	}
	if fields := strings.Fields(lines[1]); !slices.Equal(fields, []string{"TARGET", "NODE", "REACHABLE", "INSTALLED", "VERSION"}) {
		t.Fatalf("header = %v", fields)
	}
	// Rows hold registry input order with exact per-column values, so a
	// reachable-but-bare host can never pass as unreachable (or vice versa).
	wantRows := [][]string{
		{"bob@srv.tail.ts.net", "srv", "yes", "yes", "1.4.2"},
		{"dave@bare.tail.ts.net", "bare", "yes", "no", "-"},
		{"carol@lap.tail.ts.net", "lap", "no", "no", "-"},
	}
	for i, want := range wantRows {
		if got := strings.Fields(lines[2+i]); !slices.Equal(got, want) {
			t.Fatalf("row %d = %v, want %v", i, got, want)
		}
	}
}
