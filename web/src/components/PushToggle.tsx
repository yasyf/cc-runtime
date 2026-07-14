import { useEffect, useState } from 'react';
import { currentSubscription, pushSupported, subscribe, unsubscribe } from '../push';

export function PushToggle({ onError }: { onError: (err: Error) => void }) {
  const supported = pushSupported();
  const [subscribed, setSubscribed] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!supported) return;
    let active = true;
    void currentSubscription().then((sub) => {
      if (active) setSubscribed(sub !== null);
    });
    return () => {
      active = false;
    };
  }, [supported]);

  if (!supported) return null;

  async function toggle() {
    setBusy(true);
    try {
      if (subscribed) {
        await unsubscribe();
        setSubscribed(false);
      } else {
        await subscribe();
        setSubscribed(true);
      }
    } catch (err) {
      onError(err instanceof Error ? err : new Error(String(err)));
    } finally {
      setBusy(false);
    }
  }

  return (
    <button
      type="button"
      className={`push-toggle${subscribed ? ' on' : ''}`}
      aria-pressed={subscribed}
      disabled={busy}
      onClick={toggle}
      title={subscribed ? 'Push notifications on' : 'Enable push notifications'}
    >
      {subscribed ? 'Push on' : 'Enable push'}
    </button>
  );
}
