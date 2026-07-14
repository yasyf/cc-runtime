import { useQuery } from '@tanstack/react-query';
import { fetchPending, feedKey, pendingKey, queryClient } from '../api';
import { emptyFeed, useEventStream } from '../stream';
import { parseQuestion } from '../reduce';
import type { FeedState, PendingReply } from '../events';
import { QuestionCard } from './QuestionCard';
import { NotificationFeed } from './NotificationFeed';

export function SubjectDetail({ subject }: { subject: string }) {
  const stream = useEventStream();

  const pending = useQuery<PendingReply>({
    queryKey: pendingKey(subject),
    queryFn: () => fetchPending(subject),
    staleTime: 5_000,
  });

  const { data: feed } = useQuery<FeedState>({
    queryKey: feedKey(subject),
    // No get-feed endpoint; the cache is built by the SSE replay. A refetch just
    // returns the current reduced state so it never wipes it.
    queryFn: () => queryClient.getQueryData<FeedState>(feedKey(subject)) ?? emptyFeed(),
    initialData: emptyFeed,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  const onError = (err: Error) => stream.notify({ kind: 'error', text: err.message });
  const questions = (pending.data?.questions ?? []).map(parseQuestion);

  return (
    <div className="detail">
      <section className="detail-questions">
        <h2 className="detail-title">Open questions</h2>
        {questions.length > 0 ? (
          questions.map((open) => (
            <QuestionCard key={open.questionId} subject={subject} open={open} onError={onError} />
          ))
        ) : pending.isLoading ? (
          <p className="detail-hint">Loading…</p>
        ) : (
          <p className="detail-hint">Nothing to answer right now.</p>
        )}
      </section>

      <section className="detail-feed">
        <h2 className="detail-title">Notifications</h2>
        <NotificationFeed notifications={feed.notifications} />
      </section>
    </div>
  );
}
