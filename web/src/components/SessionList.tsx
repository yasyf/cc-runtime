import type { Session } from '../events';

function scopeLabel(scope: string): string {
  const trimmed = scope.replace(/\/+$/, '');
  const base = trimmed.split('/').pop();
  return base || scope || 'session';
}

export interface SessionListProps {
  sessions: Session[];
  selected: string | null;
  onSelect: (subject: string) => void;
}

export function SessionList({ sessions, selected, onSelect }: SessionListProps) {
  if (sessions.length === 0) {
    return <p className="sidebar-empty">No active sessions.</p>;
  }
  return (
    <ul className="session-list">
      {sessions.map((s) => (
        <li key={s.subject_id}>
          <button
            type="button"
            className={`session-item${s.subject_id === selected ? ' on' : ''}`}
            aria-current={s.subject_id === selected}
            onClick={() => onSelect(s.subject_id)}
          >
            <span className="session-row">
              <span className="session-name" title={s.scope}>
                {scopeLabel(s.scope)}
              </span>
              {s.pending > 0 && <span className="session-count">{s.pending}</span>}
            </span>
            <span className={`session-status status-${s.status}`}>{s.status}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}
