package mesh

import (
	"slices"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		id   string
		in   string
		want string
	}{
		{"plain", "foo", "'foo'"},
		{"empty", "", "''"},
		{"spaces", "a b c", "'a b c'"},
		{"flag", "--no-recurse", "'--no-recurse'"},
		{"target", "alice@mac.tail.ts.net", "'alice@mac.tail.ts.net'"},
		{"single-quote", "it's", `'it'\''s'`},
		{"only-quote", "'", `''\'''`},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := ShellQuote(c.in); got != c.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSSHArgv(t *testing.T) {
	got := SSHArgv("alice@mac.tail.ts.net", []string{"command", "-v", "cc-runtime"})
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		"alice@mac.tail.ts.net",
		"'command' '-v' 'cc-runtime'",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SSHArgv =\n  %#v\nwant\n  %#v", got, want)
	}
}

func TestSSHArgvQuotesInverseRegistration(t *testing.T) {
	got := SSHArgv("bob@srv.tail.ts.net", []string{"cc-runtime", "host", "add", "alice@mac.tail.ts.net", "--no-recurse"})
	remote := got[len(got)-1]
	want := "'cc-runtime' 'host' 'add' 'alice@mac.tail.ts.net' '--no-recurse'"
	if remote != want {
		t.Fatalf("remote command = %q, want %q", remote, want)
	}
	if got[len(got)-2] != "bob@srv.tail.ts.net" {
		t.Fatalf("target = %q, want the ssh target before the remote command", got[len(got)-2])
	}
}

func TestHostNode(t *testing.T) {
	cases := []struct {
		id     string
		target string
		want   string
	}{
		{"user-fqdn", "alice@mac.tail.ts.net", "mac"},
		{"local", "mac.local", "mac"},
		{"bare", "mac", "mac"},
		{"user-bare", "alice@mac", "mac"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := HostNode(c.target); got != c.want {
				t.Fatalf("HostNode(%q) = %q, want %q", c.target, got, c.want)
			}
		})
	}
}
