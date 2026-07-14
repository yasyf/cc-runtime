package access

import (
	"os"
	"testing"
)

func TestEnsureVAPIDMintsOnceAndPersists(t *testing.T) {
	st := Store{Dir: t.TempDir()}

	first, err := st.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID: %v", err)
	}
	if first.Public == "" || first.Private == "" {
		t.Fatalf("minted keys = %+v, want non-empty public and private", first)
	}
	if first.Public == first.Private {
		t.Fatal("public and private keys must differ")
	}
	info, err := os.Stat(st.VAPIDPath())
	if err != nil {
		t.Fatalf("stat vapid file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("vapid file mode = %o, want 600", got)
	}

	second, err := st.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID again: %v", err)
	}
	if second != first {
		t.Fatalf("second EnsureVAPID = %+v, want the persisted %+v", second, first)
	}

	other, err := Store{Dir: t.TempDir()}.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID other store: %v", err)
	}
	if other == first {
		t.Fatal("distinct stores must mint distinct keypairs")
	}
}

func TestEnsureVAPIDFailsOnBadFile(t *testing.T) {
	for _, tc := range []struct {
		id      string
		content string
	}{
		{id: "corrupt json", content: "{not json"},
		{id: "missing key material", content: `{"public":"","private":""}`},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := Store{Dir: t.TempDir()}
			if err := os.WriteFile(st.VAPIDPath(), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write vapid file: %v", err)
			}
			if _, err := st.EnsureVAPID(); err == nil {
				t.Fatal("EnsureVAPID on a bad file must fail loudly, got nil error")
			}
		})
	}
}
