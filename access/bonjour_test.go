package access

import "testing"

func TestIsLoopbackBind(t *testing.T) {
	tests := []struct {
		bind string
		want bool
	}{
		{"", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.5", false},
	}
	for _, tt := range tests {
		if got := IsLoopbackBind(tt.bind); got != tt.want {
			t.Errorf("IsLoopbackBind(%q) = %v, want %v", tt.bind, got, tt.want)
		}
	}
}

func TestBonjourHookNilForLoopback(t *testing.T) {
	if BonjourHook("") != nil {
		t.Error("BonjourHook(\"\") is non-nil, want nil (loopback advertises nothing)")
	}
	if BonjourHook("127.0.0.1") != nil {
		t.Error("BonjourHook(loopback) is non-nil, want nil")
	}
	if BonjourHook("0.0.0.0") == nil {
		t.Error("BonjourHook(0.0.0.0) is nil, want a hook")
	}
}
