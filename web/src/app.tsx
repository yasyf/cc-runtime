import { useCallback, useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { AppShell, ConnectionFrame, ToastStack } from '@cc-interact/react';
import { fetchSessions, sessionsKey } from './api';
import { EventStreamProvider, useEventStream } from './stream';
import type { Session, SessionsReply } from './events';
import { SessionList } from './components/SessionList';
import { SubjectDetail } from './components/SubjectDetail';
import { ThemeToggle } from './components/ThemeToggle';
import { PushToggle } from './components/PushToggle';

function subjectFromUrl(): string | null {
  if (typeof window === 'undefined') return null;
  return new URLSearchParams(window.location.search).get('subject');
}

function writeSubjectUrl(subject: string | null): void {
  const url = new URL(window.location.href);
  if (subject) url.searchParams.set('subject', subject);
  else url.searchParams.delete('subject');
  window.history.replaceState(null, '', url.pathname + url.search + url.hash);
}

interface HeaderProps {
  connected?: boolean;
  peerPresent?: boolean | null;
  onError: (err: Error) => void;
}

function Header({ connected, peerPresent, onError }: HeaderProps) {
  return (
    <header className="topbar">
      <span className="brand">cc-runtime</span>
      <span className="topbar-spacer" />
      {connected !== undefined && <ConnectionFrame connected={connected} />}
      {peerPresent != null && (
        <span
          className={`peer peer-${peerPresent ? 'on' : 'off'}`}
          title={peerPresent ? 'agent connected' : 'agent offline'}
        >
          agent
        </span>
      )}
      <PushToggle onError={onError} />
      <ThemeToggle />
    </header>
  );
}

interface ShellProps {
  sessions: Session[];
  selected: string | null;
  onSelect: (subject: string) => void;
  onError: (err: Error) => void;
}

function BareShell({ sessions, selected, onSelect, onError }: ShellProps) {
  return (
    <AppShell
      header={<Header onError={onError} />}
      sidebar={<SessionList sessions={sessions} selected={selected} onSelect={onSelect} />}
      main={
        <div className="placeholder">
          <p className="placeholder-lead">Pick a session to answer its questions.</p>
        </div>
      }
    />
  );
}

function ConnectedInner({ subject, sessions, selected, onSelect, onError }: ShellProps & { subject: string }) {
  const stream = useEventStream();
  return (
    <>
      <AppShell
        header={
          <Header connected={stream.connected} peerPresent={stream.peerPresent} onError={onError} />
        }
        sidebar={<SessionList sessions={sessions} selected={selected} onSelect={onSelect} />}
        main={<SubjectDetail subject={subject} />}
      />
      <ToastStack notifications={stream.notifications} onDismiss={stream.dismiss} />
    </>
  );
}

function ConnectedShell({ subject, ...rest }: ShellProps & { subject: string }) {
  return (
    <EventStreamProvider subject={subject}>
      <ConnectedInner subject={subject} {...rest} />
    </EventStreamProvider>
  );
}

export function App() {
  const [selected, setSelected] = useState<string | null>(subjectFromUrl);
  const [appError, setAppError] = useState<string | null>(null);

  const sessions = useQuery<SessionsReply>({
    queryKey: sessionsKey(),
    queryFn: fetchSessions,
    refetchInterval: 4_000,
    refetchOnWindowFocus: true,
  });

  const select = useCallback((subject: string | null) => {
    setSelected(subject);
    writeSubjectUrl(subject);
  }, []);

  const onError = useCallback((err: Error) => setAppError(err.message), []);

  useEffect(() => {
    if (!('serviceWorker' in navigator)) return;
    const onMessage = (e: MessageEvent) => {
      const data = e.data as { type?: string; subject?: string } | null;
      if (data?.type === 'open-subject' && data.subject) select(data.subject);
    };
    navigator.serviceWorker.addEventListener('message', onMessage);
    return () => navigator.serviceWorker.removeEventListener('message', onMessage);
  }, [select]);

  const list = sessions.data?.subjects ?? [];
  const error = appError ?? (sessions.isError ? sessions.error.message : null);

  return (
    <>
      {error && (
        <div className="app-error" role="alert">
          <span>{error}</span>
          <button type="button" aria-label="dismiss" onClick={() => setAppError(null)}>
            ×
          </button>
        </div>
      )}
      {selected ? (
        <ConnectedShell
          key={selected}
          subject={selected}
          sessions={list}
          selected={selected}
          onSelect={select}
          onError={onError}
        />
      ) : (
        <BareShell sessions={list} selected={null} onSelect={select} onError={onError} />
      )}
    </>
  );
}
