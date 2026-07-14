import { useTheme } from '../theme';
import type { ThemeMode } from '../theme';

function ThemeIcon({ mode }: { mode: ThemeMode }) {
  const props = {
    width: 16,
    height: 16,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    'aria-hidden': true,
  };
  if (mode === 'light') {
    return (
      <svg {...props}>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
      </svg>
    );
  }
  if (mode === 'dark') {
    return (
      <svg {...props}>
        <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
      </svg>
    );
  }
  return (
    <svg {...props}>
      <rect x="3" y="4" width="18" height="12" rx="1" />
      <path d="M8 20h8M12 16v4" />
    </svg>
  );
}

const THEME_MODES: ThemeMode[] = ['system', 'light', 'dark'];
const THEME_LABELS: Record<ThemeMode, string> = {
  system: 'Auto theme',
  light: 'Light theme',
  dark: 'Dark theme',
};

export function ThemeToggle() {
  const { mode, set } = useTheme();
  return (
    <span className="theme-toggle" role="group" aria-label="color theme">
      {THEME_MODES.map((m) => (
        <button
          key={m}
          type="button"
          className={`theme-seg${mode === m ? ' on' : ''}`}
          aria-pressed={mode === m}
          aria-label={THEME_LABELS[m]}
          title={THEME_LABELS[m]}
          onClick={() => set(m)}
        >
          <ThemeIcon mode={m} />
        </button>
      ))}
    </span>
  );
}
