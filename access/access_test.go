package access

import (
	"os"
	"regexp"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}

	cfg, err := s.ReadConfig()
	if err != nil {
		t.Fatalf("ReadConfig (absent): %v", err)
	}
	if cfg.Bind != "" {
		t.Fatalf("absent config Bind = %q, want \"\"", cfg.Bind)
	}

	if err := s.WriteConfig(Config{Bind: BindLAN}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err = s.ReadConfig()
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
	s := Store{Dir: t.TempDir()}
	if err := os.WriteFile(s.ConfigPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadConfig(); err == nil {
		t.Fatal("ReadConfig on corrupt file succeeded, want error")
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
