import { useCallback, useEffect, useState } from 'react';

export type ThemeMode = 'system' | 'light' | 'dark';

// Duplicated by the FOUC script in index.html; keep both literals in sync.
const STORAGE_KEY = 'cc-runtime:theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';

export function resolveMode(mode: ThemeMode, systemDark: boolean): 'light' | 'dark' {
  if (mode !== 'system') return mode;
  return systemDark ? 'dark' : 'light';
}

function readStored(): ThemeMode {
  const raw = localStorage.getItem(STORAGE_KEY);
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : 'system';
}

export interface Theme {
  mode: ThemeMode;
  resolved: 'light' | 'dark';
  set: (mode: ThemeMode) => void;
}

export function useTheme(): Theme {
  const [mode, setMode] = useState<ThemeMode>(readStored);
  const [systemDark, setSystemDark] = useState(() => window.matchMedia(DARK_QUERY).matches);

  useEffect(() => {
    const mql = window.matchMedia(DARK_QUERY);
    const onChange = (e: MediaQueryListEvent) => setSystemDark(e.matches);
    mql.addEventListener('change', onChange);
    return () => mql.removeEventListener('change', onChange);
  }, []);

  useEffect(() => {
    const root = document.documentElement;
    if (mode === 'system') {
      delete root.dataset.theme;
      localStorage.removeItem(STORAGE_KEY);
    } else {
      root.dataset.theme = mode;
      localStorage.setItem(STORAGE_KEY, mode);
    }
  }, [mode]);

  const set = useCallback((next: ThemeMode) => setMode(next), []);

  return { mode, resolved: resolveMode(mode, systemDark), set };
}
