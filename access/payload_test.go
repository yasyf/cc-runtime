package access

import (
	"encoding/json"
	"testing"
)

func TestComposePairPayload(t *testing.T) {
	urls := []string{"https://192.168.1.5:25444", "https://host.tail1234.ts.net:25443"}
	p, raw, err := ComposePairPayload("mac-studio", urls, "deadbeef", "ab12")
	if err != nil {
		t.Fatalf("ComposePairPayload: %v", err)
	}
	if p.V != 1 {
		t.Fatalf("V = %d, want 1", p.V)
	}
	// Compact JSON in declaration order — the shape the QR encodes.
	want := `{"v":1,"name":"mac-studio","urls":["https://192.168.1.5:25444",` +
		`"https://host.tail1234.ts.net:25443"],"token":"deadbeef","fp":"ab12"}`
	if raw != want {
		t.Fatalf("payload = %q, want %q", raw, want)
	}

	var back PairPayload
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if back.V != 1 || back.Name != "mac-studio" || back.Token != "deadbeef" || back.CertFP != "ab12" {
		t.Fatalf("decoded payload = %+v", back)
	}
	if len(back.URLs) != 2 || back.URLs[0] != urls[0] || back.URLs[1] != urls[1] {
		t.Fatalf("decoded URLs = %v, want %v", back.URLs, urls)
	}
}
