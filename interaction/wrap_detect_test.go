package interaction

import (
	"context"
	"testing"

	"github.com/yasyf/cc-interact/channel"
)

// TestLaunchedViaWrap covers the env-var detection seam: a wrap launch sets
// WrapEnvVar to the WrapSentinel in the child environment, and that variable
// propagates to the MCP-launched channel subprocess. Detection is true only on an
// exact match, and false when the variable is unset or carries a stale value.
func TestLaunchedViaWrap(t *testing.T) {
	t.Run("env matches sentinel -> true", func(t *testing.T) {
		t.Setenv(WrapEnvVar, WrapSentinel)
		if !launchedViaWrap() {
			t.Fatalf("launchedViaWrap = false with %s=%q, want true", WrapEnvVar, WrapSentinel)
		}
	})

	t.Run("env unset -> false", func(t *testing.T) {
		t.Setenv(WrapEnvVar, "")
		if launchedViaWrap() {
			t.Fatalf("launchedViaWrap = true with %s empty, want false", WrapEnvVar)
		}
	})

	t.Run("env mismatched -> false", func(t *testing.T) {
		t.Setenv(WrapEnvVar, "cc-runtime-wrap:v0")
		if launchedViaWrap() {
			t.Fatalf("launchedViaWrap = true with a stale %s value, want false", WrapEnvVar)
		}
	})
}

// TestChannelToolsInstructionsPerWrapDetection asserts ChannelTools returns the
// trimmed wrappedInstructions when WrapEnvVar carries the sentinel and the full
// soft-steer channelInstructions otherwise. The ask/notify tools are advertised in
// both cases. ChannelTools constructs the tools without dialing the daemon (the
// handlers dial lazily), so no running daemon is needed.
func TestChannelToolsInstructionsPerWrapDetection(t *testing.T) {
	const (
		session = "sess-instr"
		scope   = "/Users/yasyf/Code/cc-runtime"
		pid     = 4242
	)

	t.Run("wrapped -> minimal instructions", func(t *testing.T) {
		t.Setenv(WrapEnvVar, WrapSentinel)

		tools, _, instructions, err := ChannelTools(context.Background(), session, scope, pid)
		if err != nil {
			t.Fatalf("ChannelTools: %v", err)
		}
		if instructions != wrappedInstructions {
			t.Fatalf("wrapped instructions = %q, want wrappedInstructions", instructions)
		}
		assertHasAskAndNotify(t, tools)
	})

	t.Run("not wrapped -> full soft-steer instructions", func(t *testing.T) {
		t.Setenv(WrapEnvVar, "")

		tools, _, instructions, err := ChannelTools(context.Background(), session, scope, pid)
		if err != nil {
			t.Fatalf("ChannelTools: %v", err)
		}
		if instructions != channelInstructions {
			t.Fatalf("unwrapped instructions = %q, want full channelInstructions", instructions)
		}
		assertHasAskAndNotify(t, tools)
	})
}

func assertHasAskAndNotify(t *testing.T, tools []channel.Tool) {
	t.Helper()
	var hasAsk, hasNotify bool
	for _, tl := range tools {
		switch tl.Name {
		case "ask":
			hasAsk = true
		case "notify":
			hasNotify = true
		}
	}
	if !hasAsk || !hasNotify {
		t.Fatalf("tools missing ask/notify: ask=%v notify=%v", hasAsk, hasNotify)
	}
}
