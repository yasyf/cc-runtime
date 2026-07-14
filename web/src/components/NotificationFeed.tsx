import type { NotificationEntry } from '../events';

function formatTime(at: number): string {
  return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function NotificationFeed({ notifications }: { notifications: NotificationEntry[] }) {
  if (notifications.length === 0) {
    return <p className="feed-empty">No notifications yet.</p>;
  }
  const ordered = [...notifications].reverse();
  return (
    <ul className="feed">
      {ordered.map((n) => (
        <li key={n.id} className={`feed-item${n.urgency === 'high' ? ' urgent' : ''}`}>
          <span className="feed-msg">{n.message}</span>
          <time className="feed-time">{formatTime(n.at)}</time>
        </li>
      ))}
    </ul>
  );
}
