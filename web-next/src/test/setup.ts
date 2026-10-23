import '@testing-library/jest-dom/vitest';

// Newer Node releases expose a process-level localStorage object, but in
// some CI/dev shells it can be present without the browser Storage methods
// jsdom tests expect. Normalize that once for the whole test environment.
if (
  typeof globalThis.localStorage === 'undefined' ||
  typeof globalThis.localStorage.clear !== 'function'
) {
  const storage = createMemoryStorage();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: storage,
  });
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: storage,
  });
}

// jsdom does not implement EventSource; tests stub it where needed.
if (typeof (globalThis as { EventSource?: unknown }).EventSource === 'undefined') {
  class StubEventSource {
    onopen: ((ev: Event) => void) | null = null;
    onmessage: ((ev: MessageEvent) => void) | null = null;
    onerror: ((ev: Event) => void) | null = null;
    private listeners = new Map<string, Set<(ev: MessageEvent) => void>>();
    public readonly url: string;
    public readyState = 0;
    constructor(url: string) {
      this.url = url;
    }
    addEventListener(type: string, fn: (ev: MessageEvent) => void) {
      let set = this.listeners.get(type);
      if (!set) {
        set = new Set();
        this.listeners.set(type, set);
      }
      set.add(fn);
    }
    removeEventListener(type: string, fn: (ev: MessageEvent) => void) {
      this.listeners.get(type)?.delete(fn);
    }
    close() {
      this.readyState = 2;
    }
    /** Test helper: dispatch a named event into this stub. */
    __emit(type: string, data: unknown) {
      const ev = new MessageEvent(type, { data: JSON.stringify(data) });
      this.listeners.get(type)?.forEach((fn) => fn(ev));
    }
  }
  (globalThis as { EventSource?: unknown }).EventSource = StubEventSource;
}

function createMemoryStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    clear() {
      values.clear();
    },
    getItem(key: string) {
      return values.get(key) ?? null;
    },
    key(index: number) {
      return Array.from(values.keys())[index] ?? null;
    },
    removeItem(key: string) {
      values.delete(key);
    },
    setItem(key: string, value: string) {
      values.set(key, String(value));
    },
  };
}
