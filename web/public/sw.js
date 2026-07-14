// cc-runtime service worker: render Web Push frames and route a tap back into
// the app. The frame is the access.PushPayload {type, subject, title, body,
// urgency}; it never carries the pair token.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));

function titleFor(payload) {
  if (payload.title) return payload.title;
  return payload.type === 'interaction.question' ? 'Question waiting' : 'cc-runtime';
}

self.addEventListener('push', (event) => {
  if (!event.data) return;
  let payload;
  try {
    payload = event.data.json();
  } catch (e) {
    payload = { body: event.data.text() };
  }
  const isQuestion = payload.type === 'interaction.question';
  event.waitUntil(
    self.registration.showNotification(titleFor(payload), {
      body: payload.body || '',
      tag: payload.subject || 'cc-runtime',
      requireInteraction: isQuestion,
      data: { subject: payload.subject || '' },
    }),
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const subject = event.notification.data && event.notification.data.subject;
  const target = subject ? `/?subject=${encodeURIComponent(subject)}` : '/';
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if ('focus' in client) {
          client.postMessage({ type: 'open-subject', subject });
          return client.focus();
        }
      }
      return self.clients.openWindow(target);
    }),
  );
});
