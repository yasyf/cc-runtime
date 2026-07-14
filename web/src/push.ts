// Web Push subscribe/unsubscribe. The service worker at /sw.js renders frames; the
// VAPID key comes from the daemon and the subscription posts back to it.

import { request } from '@cc-interact/react';
import { authHeader } from './token';

interface VapidKeyReply {
  key: string;
}

export function pushSupported(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  );
}

function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4);
  const normalized = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(normalized);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i += 1) out[i] = raw.charCodeAt(i);
  return out;
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration();
  return existing ?? navigator.serviceWorker.register('/sw.js', { scope: '/' });
}

export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration();
  return reg ? reg.pushManager.getSubscription() : null;
}

// subscribe registers the worker, secures notification permission, subscribes to
// the daemon's VAPID key, and posts the subscription. It is idempotent server-side.
export async function subscribe(): Promise<PushSubscription> {
  const permission = await Notification.requestPermission();
  if (permission !== 'granted') throw new Error('notification permission denied');
  const reg = await registration();
  const { key } = await request<VapidKeyReply>('/api/push/vapid-key', { headers: authHeader() });
  const existing = await reg.pushManager.getSubscription();
  const sub =
    existing ??
    (await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(key),
    }));
  await request<{ ok: boolean }>('/api/push/subscriptions', {
    method: 'POST',
    headers: authHeader(),
    body: JSON.stringify(sub.toJSON()),
  });
  return sub;
}

export async function unsubscribe(): Promise<void> {
  const sub = await currentSubscription();
  if (sub) await sub.unsubscribe();
}
