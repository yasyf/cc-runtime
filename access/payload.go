package access

import "encoding/json"

// PairPayload is the compact JSON the QR encodes and the pair command also
// prints as copyable text: the protocol version, the host's name, every URL a
// client can reach the daemon at (LAN HTTPS first, tailnet HTTPS when
// available), the bearer token the client presents, and the self-signed LAN
// certificate's SHA-256 fingerprint the client pins to authenticate the LAN
// leg.
type PairPayload struct {
	V      int      `json:"v"`
	Name   string   `json:"name"`
	URLs   []string `json:"urls"`
	Token  string   `json:"token"`
	CertFP string   `json:"fp"`
}

// ComposePairPayload builds the pairing payload and its compact JSON encoding.
func ComposePairPayload(name string, urls []string, token, certFP string) (PairPayload, string, error) {
	p := PairPayload{V: 1, Name: name, URLs: urls, Token: token, CertFP: certFP}
	raw, err := json.Marshal(p)
	if err != nil {
		return PairPayload{}, "", err
	}
	return p, string(raw), nil
}
