package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDetectSelfRunning(t *testing.T) {
	r := NewMockRunner().
		On("tailscale status", `{"BackendState":"Running","Self":{"DNSName":"mac.tail.ts.net."}}`, nil).
		On("id -un", "alice\n", nil)
	got, err := DetectSelf(context.Background(), r)
	if err != nil {
		t.Fatalf("DetectSelf: %v", err)
	}
	if want := "alice@mac.tail.ts.net"; got != want {
		t.Fatalf("DetectSelf = %q, want %q", got, want)
	}
}

func TestDetectSelfTailscaleAbsent(t *testing.T) {
	r := NewMockRunner().On("tailscale status", "", errors.New("exec: \"tailscale\": executable file not found in $PATH"))
	_, err := DetectSelf(context.Background(), r)
	if err == nil {
		t.Fatal("DetectSelf must error when tailscale is absent")
	}
	if !strings.Contains(err.Error(), "--self") {
		t.Fatalf("err = %v, want it to point at --self", err)
	}
}

func TestDetectSelfStopped(t *testing.T) {
	r := NewMockRunner().On("tailscale status", `{"BackendState":"Stopped","Self":{"DNSName":"mac.tail.ts.net."}}`, nil)
	_, err := DetectSelf(context.Background(), r)
	if err == nil {
		t.Fatal("DetectSelf must error when the backend is not Running")
	}
	if !strings.Contains(err.Error(), "Stopped") {
		t.Fatalf("err = %v, want it to name the backend state", err)
	}
}

func TestDetectSelfNoMagicDNS(t *testing.T) {
	r := NewMockRunner().On("tailscale status", `{"BackendState":"Running","Self":{"DNSName":""}}`, nil)
	_, err := DetectSelf(context.Background(), r)
	if err == nil {
		t.Fatal("DetectSelf must error when MagicDNS is off")
	}
	if !strings.Contains(err.Error(), "MagicDNS") {
		t.Fatalf("err = %v, want it to mention MagicDNS", err)
	}
}
