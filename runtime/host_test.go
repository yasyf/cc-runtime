package runtime

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-runtime/mesh"
)

func TestPrintHostListEmpty(t *testing.T) {
	var out bytes.Buffer
	reg := &mesh.Registry{Self: "alice@mac.tail.ts.net"}
	if err := printHostList(context.Background(), &out, reg, sshRunner); err != nil {
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
	reg := &mesh.Registry{
		Self:  "alice@mac.tail.ts.net",
		Hosts: []string{"bob@srv.tail.ts.net", "carol@lap.tail.ts.net"},
	}
	dial := func(target string) mesh.Runner {
		m := mesh.NewMockRunner()
		if target == "carol@lap.tail.ts.net" {
			return m.On("true", "", errors.New("down"))
		}
		return m.On("true", "", nil).
			On("command -v cc-runtime", "/usr/local/bin/cc-runtime\n", nil).
			On("version", "1.4.2\n", nil)
	}
	if err := printHostList(context.Background(), &out, reg, dial); err != nil {
		t.Fatalf("printHostList: %v", err)
	}
	got := out.String()
	for _, want := range []string{"bob@srv.tail.ts.net", "srv", "1.4.2", "carol@lap.tail.ts.net", "lap"} {
		if !strings.Contains(got, want) {
			t.Fatalf("host list missing %q:\n%s", want, got)
		}
	}
	// carol is down: her row shows no/-, bob is up: yes.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	var bobLine, carolLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "bob@") {
			bobLine = l
		}
		if strings.HasPrefix(l, "carol@") {
			carolLine = l
		}
	}
	if !strings.Contains(bobLine, "yes") {
		t.Fatalf("bob should be reachable: %q", bobLine)
	}
	if !strings.Contains(carolLine, "no") {
		t.Fatalf("carol should be unreachable: %q", carolLine)
	}
}
