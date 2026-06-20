package interaction

import "encoding/json"

// wireEvent builds the self-contained event frame that streams to the browser
// and the Claude-side channel consumer: {type, ...payload}. The type
// discriminator must live inside the payload because the SSE plane streams only
// the raw event payload — the event's Type column never reaches the wire. The
// projection tables keep the bare domain payload; only the streamed event frame
// carries type.
func wireEvent(typ string, payload any) json.RawMessage {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	m["type"] = typ
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}
