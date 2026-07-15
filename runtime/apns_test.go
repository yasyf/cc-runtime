package runtime

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPNSSetRequiresEveryFlag(t *testing.T) {
	root := Root()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"apns", "set", "--key", "/keys/AuthKey.p8"})
	err := root.Execute()
	if err == nil {
		t.Fatal("apns set without required flags must fail, got nil error")
	}
	for _, flag := range []string{"key-id", "team-id", "bundle-id"} {
		if !strings.Contains(err.Error(), flag) {
			t.Fatalf("error = %v, want the missing %q flag named", err, flag)
		}
	}
}

func TestAPNSSetRejectsABrokenKeyBeforePersisting(t *testing.T) {
	root := Root()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{
		"apns", "set",
		"--key", filepath.Join(t.TempDir(), "missing.p8"),
		"--key-id", "ABC123",
		"--team-id", "TEAM99",
		"--bundle-id", "com.example.cc",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "apns key") {
		t.Fatalf("apns set with a missing key = %v, want a key load failure", err)
	}
}
