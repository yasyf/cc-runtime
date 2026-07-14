// The feed reduction: notification frames append to the SSE-built cache; every
// other frame passes through (questions come from REST, presence from the stream).

import type { FeedState, NotificationEntry, PendingQuestion, OpenQuestion, WireFrame } from './events';
import type { QuestionPayload } from './events';

let notifSeq = 0;

export function emptyFeed(): FeedState {
  return { notifications: [] };
}

function toNotification(message: string, urgency: string | undefined): NotificationEntry {
  return { id: `nf${++notifSeq}`, message, urgency, at: Date.now() };
}

export function applyFrame(state: FeedState, frame: WireFrame): FeedState {
  if (frame.type !== 'interaction.notification') return state;
  return { notifications: [...state.notifications, toNotification(frame.message, frame.urgency)] };
}

// parseQuestion parses a REST PendingQuestion's JSON payload string for rendering.
export function parseQuestion(pq: PendingQuestion): OpenQuestion {
  const question = JSON.parse(pq.payload) as QuestionPayload;
  return { questionId: pq.question_id, question };
}
