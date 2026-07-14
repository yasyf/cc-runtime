// One EventSource per subject: notification frames build the feed, question/answer
// frames invalidate the REST snapshots, channel.changed drives peer presence.

import { createEventStream } from '@cc-interact/react';
import { applyFrame, emptyFeed } from './reduce';
import { feedKey, pendingKey, sessionsKey } from './api';
import { withToken } from './token';
import type { FeedState, WireFrame } from './events';

export { emptyFeed };

export const { EventStreamProvider, useEventStream } = createEventStream<WireFrame, FeedState>({
  queryKey: (subject) => feedKey(subject),
  url: (subject) => withToken(`/events?session=${encodeURIComponent(subject)}`),
  reduce: (cache, frame) => applyFrame(cache, frame),
  toast: (frame) => {
    switch (frame.type) {
      case 'interaction.question':
        return { kind: 'info', text: frame.prompt || frame.header || 'New question' };
      case 'interaction.notification':
        return { kind: frame.urgency === 'high' ? 'warn' : 'info', text: frame.message };
      default:
        return null;
    }
  },
  // Silence the from-zero replay; the caught-up marker gates the live tail.
  highWaterSeq: () => Number.POSITIVE_INFINITY,
  peerPresence: (frame) => (frame.type === 'channel.changed' ? frame.connected : null),
  onEvent: (frame, ctx) => {
    if (frame.type === 'interaction.question' || frame.type === 'interaction.answer') {
      void ctx.queryClient.invalidateQueries({ queryKey: pendingKey(ctx.subject) });
      void ctx.queryClient.invalidateQueries({ queryKey: sessionsKey() });
    }
  },
});
