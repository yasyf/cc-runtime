// Node 25's experimental global `localStorage` shadows jsdom's with a no-op stub;
// install a real in-memory Storage so the token module tests hit a faithful
// boundary.

class MemoryStorage implements Storage {
  private store = new Map<string, string>();

  get length(): number {
    return this.store.size;
  }
  clear(): void {
    this.store.clear();
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) as string) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  key(index: number): string | null {
    return [...this.store.keys()][index] ?? null;
  }
}

const storage = new MemoryStorage();
for (const target of [globalThis, window]) {
  Object.defineProperty(target, 'localStorage', { value: storage, configurable: true, writable: true });
}
