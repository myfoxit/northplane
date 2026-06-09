// Vitest global setup: jest-dom matchers, MSW server lifecycle, and the
// minimal jsdom polyfills that Radix/cmdk and the app's components need but
// jsdom doesn't implement. Only polyfills the tests actually exercise.
import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './msw'

// —— MSW lifecycle ————————————————————————————————————————————————————
// Fail loudly on any request without a matching handler so tests can't pass
// against an accidental real fetch.
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => server.resetHandlers())
afterAll(() => server.close())

// —— jsdom polyfills ———————————————————————————————————————————————————

// Node ≥22 ships a global `localStorage` that is non-functional without
// --localstorage-file and shadows jsdom's implementation on the shared
// global. Replace it with a working in-memory Storage so app code and
// tests read/write the same store.
if (typeof localStorage === 'undefined' || typeof localStorage.clear !== 'function') {
  const mem = new Map<string, string>()
  const storage: Storage = {
    get length() { return mem.size },
    clear: () => mem.clear(),
    getItem: (k) => (mem.has(k) ? mem.get(k)! : null),
    key: (i) => [...mem.keys()][i] ?? null,
    removeItem: (k) => { mem.delete(k) },
    setItem: (k, v) => { mem.set(k, String(v)) },
  }
  Object.defineProperty(globalThis, 'localStorage', {
    value: storage, configurable: true, writable: true,
  })
}

// next-themes / responsive hooks read matchMedia at mount.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

// Radix popovers/charts measure with ResizeObserver.
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// Radix Select/cmdk call scrollIntoView on the active item.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}

// TanStack Router's scroll restoration calls window.scrollTo on navigation;
// jsdom logs "Not implemented" for it. Stub it to keep test output clean.
window.scrollTo = () => {}

