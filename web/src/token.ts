// The pair bearer token: fetch sends `Authorization: Bearer`, EventSource carries
// `?token=`. Read once from the page URL, persisted, then scrubbed. Loopback
// needs none.

const STORAGE_KEY = 'cc-runtime:token';

let cached: string | null | undefined;

function scrubUrl(): void {
  const url = new URL(window.location.href);
  let changed = false;
  if (url.searchParams.has('token')) {
    url.searchParams.delete('token');
    changed = true;
  }
  if (url.hash) {
    const frag = new URLSearchParams(url.hash.replace(/^#/, ''));
    if (frag.has('token')) {
      frag.delete('token');
      const rest = frag.toString();
      url.hash = rest ? `#${rest}` : '';
      changed = true;
    }
  }
  if (changed) window.history.replaceState(null, '', url.pathname + url.search + url.hash);
}

function readInitial(): string | null {
  if (typeof window === 'undefined') return null;
  const fromQuery = new URLSearchParams(window.location.search).get('token');
  const fromFragment = new URLSearchParams(window.location.hash.replace(/^#/, '')).get('token');
  const fromUrl = fromQuery ?? fromFragment;
  if (fromUrl) {
    localStorage.setItem(STORAGE_KEY, fromUrl);
    scrubUrl();
    return fromUrl;
  }
  return localStorage.getItem(STORAGE_KEY);
}

function currentToken(): string | null {
  if (cached === undefined) cached = readInitial();
  return cached;
}

export function hasToken(): boolean {
  return currentToken() !== null;
}

export function authHeader(): Record<string, string> {
  const token = currentToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

// withToken appends `?token=` for EventSource; no token returns path unchanged.
export function withToken(path: string): string {
  const token = currentToken();
  if (!token) return path;
  const sep = path.includes('?') ? '&' : '?';
  return `${path}${sep}token=${encodeURIComponent(token)}`;
}

export function resetTokenForTest(): void {
  cached = undefined;
}
