package interaction

// Option is one selectable choice in a question. Field names match Claude Code's
// native AskUserQuestion tool so the channel tool's mapping is 1:1.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// QuestionPayload is the structured ask persisted on the interaction.question
// event and projected into pending_questions.
type QuestionPayload struct {
	Header      string   `json:"header,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	Options     []Option `json:"options"`
	Prompt      string   `json:"prompt,omitempty"`
	Reasoning   string   `json:"reasoning,omitempty"`
	Diff        string   `json:"diff,omitempty"`
}

// AnswerPayload identifies its target by SubjectID+QuestionID because the
// answerer (TUI or answer command) is a foreign process carrying neither the
// agent session nor pid.
type AnswerPayload struct {
	SubjectID  string   `json:"subject_id"`
	QuestionID int64    `json:"question_id"`
	Selected   []string `json:"selected"`
	Other      string   `json:"other,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

// NotificationPayload is a one-way message appended to a subject's log.
type NotificationPayload struct {
	Message     string `json:"message"`
	Urgency     string `json:"urgency,omitempty"`
	DeliveryKey string `json:"delivery_key,omitempty"`
}
