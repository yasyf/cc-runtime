package access

import "encoding/json"

// PairPayload is the compact JSON the QR encodes and the pair command also
// prints as copyable text: the protocol version, the host's name, every URL a
// client can reach the daemon at (LAN HTTP first, tailnet HTTPS when
// available), and the bearer token the client presents.
type PairPayload struct {
	V     int      `json:"v"`
	Name  string   `json:"name"`
	URLs  []string `json:"urls"`
	Token string   `json:"token"`
}

// ComposePairPayload builds the pairing payload and its compact JSON encoding.
func ComposePairPayload(name string, urls []string, token string) (PairPayload, string, error) {
	p := PairPayload{V: 1, Name: name, URLs: urls, Token: token}
	raw, err := json.Marshal(p)
	if err != nil {
		return PairPayload{}, "", err
	}
	return p, string(raw), nil
}
