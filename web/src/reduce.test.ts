import { describe, expect, it } from 'vitest';
import { applyFrame, emptyFeed, parseQuestion } from './reduce';
import type { WireFrame } from './events';

describe('applyFrame', () => {
  it('appends a notification frame to the feed', () => {
    const frame: WireFrame = { type: 'interaction.notification', message: 'build done', urgency: 'high' };
    const next = applyFrame(emptyFeed(), frame);
    expect(next.notifications).toHaveLength(1);
    expect(next.notifications[0]?.message).toBe('build done');
    expect(next.notifications[0]?.urgency).toBe('high');
  });

  it('ignores question, answer, and presence frames', () => {
    const base = emptyFeed();
    const question: WireFrame = { type: 'interaction.question', options: [], prompt: 'pick' };
    const answer: WireFrame = { type: 'interaction.answer', subject_id: 's', question_id: 1, selected: [] };
    const presence: WireFrame = { type: 'channel.changed', connected: true };
    expect(applyFrame(base, question)).toBe(base);
    expect(applyFrame(base, answer)).toBe(base);
    expect(applyFrame(base, presence)).toBe(base);
  });

  it('appends in arrival order across multiple notifications', () => {
    let state = emptyFeed();
    state = applyFrame(state, { type: 'interaction.notification', message: 'one' });
    state = applyFrame(state, { type: 'interaction.notification', message: 'two' });
    expect(state.notifications.map((n) => n.message)).toEqual(['one', 'two']);
  });
});

describe('parseQuestion', () => {
  it('parses the JSON payload string into an OpenQuestion', () => {
    const open = parseQuestion({
      question_id: 42,
      header: 'Deploy',
      payload: JSON.stringify({ options: [{ label: 'Yes' }], prompt: 'Ship it?', multiSelect: false }),
    });
    expect(open.questionId).toBe(42);
    expect(open.question.prompt).toBe('Ship it?');
    expect(open.question.options).toEqual([{ label: 'Yes' }]);
  });
});
