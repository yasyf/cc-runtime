// The domain wire shapes and the reduced feed cache. Every field name matches the
// Go JSON exactly.

// --- Question / answer / notification payloads (mirror interaction/payload.go) ---

export interface Option {
  label: string;
  description?: string;
  preview?: string;
}

export interface QuestionPayload {
  header?: string;
  multiSelect?: boolean;
  options: Option[];
  prompt?: string;
  reasoning?: string;
  diff?: string;
}

export interface NotificationPayload {
  message: string;
  urgency?: string;
}

// AnswerBody is the POST answer body — AnswerPayload minus the path subject_id.
export interface AnswerBody {
  question_id: number;
  selected: string[];
  other?: string;
  notes?: string;
}

// --- SSE wire frames (each /events `data:`; the id is the seq / question id) ---

export type WireFrame =
  | ({ type: 'interaction.question' } & QuestionPayload)
  | {
      type: 'interaction.answer';
      subject_id: string;
      question_id: number;
      selected: string[];
      other?: string;
      notes?: string;
    }
  | ({ type: 'interaction.notification' } & NotificationPayload)
  | { type: 'channel.changed'; connected: boolean };

// --- REST shapes ---

export type SessionStatus = 'idle' | 'awaiting';

export interface Session {
  subject_id: string;
  scope: string;
  status: SessionStatus;
  pending: number;
}

export interface SessionsReply {
  subjects: Session[];
}

// payload is the full QuestionPayload as an unparsed JSON string.
export interface PendingQuestion {
  question_id: number;
  header?: string;
  payload: string;
}

export interface PendingReply {
  questions: PendingQuestion[];
}

export interface AnswerReply {
  idled: boolean;
}

// OpenQuestion is a PendingQuestion with its payload parsed for rendering.
export interface OpenQuestion {
  questionId: number;
  question: QuestionPayload;
}

// --- Reduced feed cache (SSE-built; there is no GET-notifications endpoint) ---

export interface NotificationEntry {
  id: string;
  message: string;
  urgency?: string;
  at: number;
}

export interface FeedState {
  notifications: NotificationEntry[];
}
