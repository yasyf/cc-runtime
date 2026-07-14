import { beforeEach, describe, expect, it } from 'vitest';
import { authHeader, hasToken, resetTokenForTest, withToken } from './token';

beforeEach(() => {
  localStorage.clear();
  window.history.replaceState({}, '', '/');
  resetTokenForTest();
});

describe('token', () => {
  it('returns the path unchanged and no header with no token', () => {
    expect(withToken('/events?session=x')).toBe('/events?session=x');
    expect(authHeader()).toEqual({});
    expect(hasToken()).toBe(false);
  });

  it('reads the token from the query, persists it, and scrubs the url', () => {
    window.history.replaceState({}, '', '/?token=deadbeef&subject=s1');
    resetTokenForTest();
    expect(hasToken()).toBe(true);
    expect(authHeader()).toEqual({ Authorization: 'Bearer deadbeef' });
    expect(withToken('/api/sessions')).toBe('/api/sessions?token=deadbeef');
    expect(withToken('/events?session=x')).toBe('/events?session=x&token=deadbeef');
    expect(localStorage.getItem('cc-runtime:token')).toBe('deadbeef');
    expect(window.location.search).toBe('?subject=s1');
  });

  it('reads the token from the url fragment and scrubs it', () => {
    window.history.replaceState({}, '', '/#token=cafe');
    resetTokenForTest();
    expect(authHeader()).toEqual({ Authorization: 'Bearer cafe' });
    expect(window.location.hash).toBe('');
  });

  it('falls back to the persisted token', () => {
    localStorage.setItem('cc-runtime:token', 'stored');
    resetTokenForTest();
    expect(withToken('/api/sessions')).toBe('/api/sessions?token=stored');
    expect(authHeader()).toEqual({ Authorization: 'Bearer stored' });
  });
});
