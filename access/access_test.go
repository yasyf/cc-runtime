package access

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}

	if _, err := s.ReadConfig(); err == nil {
		t.Fatal("ReadConfig accepted an absent access config")
	}

	if err := s.WriteConfig(Config{Bind: BindLAN}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	written, err := os.ReadFile(s.ConfigPath())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	want := `{"schema":"dev.yasyf.cc-runtime.access","schemaVersion":1,"schemaFingerprint":"dev.yasyf.cc-runtime.access.729d1a3bd6b99aee47bd61e156a828cb20c942754523d8f93d7b12b3d9219bc1","payload":{"bind":"0.0.0.0"}}`
	if string(written) != want {
		t.Fatalf("persisted config = %s, want %s", written, want)
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.Bind != BindLAN {
		t.Fatalf("Bind = %q, want %q", cfg.Bind, BindLAN)
	}

	info, err := os.Stat(s.ConfigPath())
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perm = %o, want 600", perm)
	}
}

func TestConfigCorruptFailsLoudly(t *testing.T) {
	valid := `{"schema":"` + accessConfigSchemaIdentity + `","schemaVersion":1,"schemaFingerprint":"` + accessConfigSchemaFingerprint + `","payload":{"bind":"127.0.0.1"}}`
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "corrupt", data: "not json"},
		{name: "old raw payload", data: `{"bind":"127.0.0.1"}`},
		{name: "missing schema", data: `{"schemaVersion":1,"schemaFingerprint":"` + accessConfigSchemaFingerprint + `","payload":{"bind":"127.0.0.1"}}`},
		{name: "missing version", data: `{"schema":"` + accessConfigSchemaIdentity + `","schemaFingerprint":"` + accessConfigSchemaFingerprint + `","payload":{"bind":"127.0.0.1"}}`},
		{name: "missing fingerprint", data: `{"schema":"` + accessConfigSchemaIdentity + `","schemaVersion":1,"payload":{"bind":"127.0.0.1"}}`},
		{name: "wrong schema", data: strings.Replace(valid, accessConfigSchemaIdentity, "dev.yasyf.other", 1)},
		{name: "old version", data: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":0`, 1)},
		{name: "new version", data: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "wrong fingerprint", data: strings.Replace(valid, accessConfigSchemaFingerprint, accessConfigSchemaIdentity+".stale", 1)},
		{name: "missing payload", data: `{"schema":"` + accessConfigSchemaIdentity + `","schemaVersion":1,"schemaFingerprint":"` + accessConfigSchemaFingerprint + `"}`},
		{name: "null payload", data: strings.Replace(valid, `{"bind":"127.0.0.1"}`, `null`, 1)},
		{name: "missing bind", data: strings.Replace(valid, `{"bind":"127.0.0.1"}`, `{}`, 1)},
		{name: "invalid bind", data: strings.Replace(valid, BindLoopback, "localhost", 1)},
		{name: "extra envelope field", data: strings.TrimSuffix(valid, "}") + `,"legacy":true}`},
		{name: "extra payload field", data: strings.Replace(valid, `"bind":"127.0.0.1"`, `"bind":"127.0.0.1","legacy":true`, 1)},
		{name: "trailing value", data: valid + ` {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Store{Dir: t.TempDir()}
			if err := os.WriteFile(s.ConfigPath(), []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadConfig(); err == nil {
				t.Fatalf("ReadConfig accepted %s", tc.data)
			}
		})
	}
}

func TestWriteConfigRejectsInvalidBind(t *testing.T) {
	for _, bind := range []string{"", "localhost", "::"} {
		s := Store{Dir: t.TempDir()}
		if err := s.WriteConfig(Config{Bind: bind}); err == nil {
			t.Fatalf("WriteConfig accepted bind %q", bind)
		}
	}
}

func TestTokenLifecycle(t *testing.T) {
	s := Store{Dir: t.TempDir()}

	tok, err := s.ReadToken()
	if err != nil {
		t.Fatalf("ReadToken (absent): %v", err)
	}
	if tok != "" {
		t.Fatalf("absent token = %q, want \"\"", tok)
	}

	minted, err := s.EnsureToken()
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(minted) {
		t.Fatalf("token = %q, want 64 hex chars", minted)
	}

	again, err := s.EnsureToken()
	if err != nil {
		t.Fatalf("EnsureToken (second): %v", err)
	}
	if again != minted {
		t.Fatalf("EnsureToken not idempotent: %q then %q", minted, again)
	}

	reset, err := s.ResetToken()
	if err != nil {
		t.Fatalf("ResetToken: %v", err)
	}
	if reset == minted {
		t.Fatal("ResetToken returned the prior token, want a fresh one")
	}
	read, err := s.ReadToken()
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if read != reset {
		t.Fatalf("ReadToken = %q, want the reset token %q", read, reset)
	}

	info, err := os.Stat(s.TokenPath())
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token perm = %o, want 600", perm)
	}
}

// TestEnsureTokenConcurrentFirstRun races first-run mints: every caller must
// return the one token that landed on disk, never a token nobody can use.
func TestEnsureTokenConcurrentFirstRun(t *testing.T) {
	s := Store{Dir: t.TempDir()}

	const racers = 8
	tokens := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = s.EnsureToken()
		}()
	}
	wg.Wait()

	onDisk, err := s.ReadToken()
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if onDisk == "" {
		t.Fatal("no token landed on disk")
	}
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if tokens[i] != onDisk {
			t.Fatalf("racer %d returned %q, want the on-disk token %q", i, tokens[i], onDisk)
		}
	}

	leftovers, err := filepath.Glob(filepath.Join(s.Dir, ".token-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp mint files left behind: %v", leftovers)
	}
}
