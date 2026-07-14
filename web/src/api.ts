// The query layer: the daemon-tuned QueryClient, cache keys, the REST fetchers,
// and the optimistic answer mutation. Auth rides on every request via authHeader.

import { createQueryClient, request, scopedKey, useOptimisticMutation } from '@cc-interact/react';
import type { UseMutationResult } from '@tanstack/react-query';
import { authHeader } from './token';
import type { AnswerBody, AnswerReply, PendingReply, SessionsReply } from './events';

export const queryClient = createQueryClient();

export const sessionsKey = () => ['sessions'] as const;
export const pendingKey = (subject: string) => scopedKey('pending', subject, undefined);
export const feedKey = (subject: string) => scopedKey('feed', subject, undefined);

const subjectPath = (subject: string) => `/api/subjects/${encodeURIComponent(subject)}`;

export function fetchSessions(): Promise<SessionsReply> {
  return request<SessionsReply>('/api/sessions', { headers: authHeader() });
}

export function fetchPending(subject: string): Promise<PendingReply> {
  return request<PendingReply>(`${subjectPath(subject)}/pending`, { headers: authHeader() });
}

export function postAnswer(subject: string, body: AnswerBody): Promise<AnswerReply> {
  return request<AnswerReply>(`${subjectPath(subject)}/answer`, {
    method: 'POST',
    headers: authHeader(),
    body: JSON.stringify(body),
  });
}

// useAnswer submits an answer, optimistically dropping the question from the
// pending cache; the SSE echo and refetch reconcile. Posts serialize per subject.
export function useAnswer(
  subject: string,
  onError: (err: Error) => void,
): UseMutationResult<AnswerReply, Error, AnswerBody> {
  return useOptimisticMutation<AnswerBody, AnswerReply, PendingReply>({
    mutationFn: (body) => postAnswer(subject, body),
    queryKey: () => pendingKey(subject),
    applyOptimistic: (cache, body) => ({
      questions: cache.questions.filter((q) => q.question_id !== body.question_id),
    }),
    invalidate: () => ({ queryKey: pendingKey(subject) }),
    onError,
    scope: subject,
  });
}
