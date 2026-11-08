// Browser storage (localStorage). Only per-device conveniences live here: the session
// token, feed filters, a listing draft and recently viewed items. Everything that
// matters is stored in PostgreSQL through the API.

import type { Listing, Session } from "./types";

function read<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function write(key: string, value: unknown): void {
  try {
    if (value === null || value === undefined) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // storage full or blocked (private mode): the app still works, it just forgets
  }
}

const KEYS = {
  session: "hm.session",
  filters: "hm.feedFilters",
  draft: "hm.listingDraft",
  recent: "hm.recentlyViewed",
} as const;

// The session is also an external store React can subscribe to (useSyncExternalStore),
// so every component sees sign-in / sign-out at once, including in other tabs.
const sessionListeners = new Set<() => void>();

export const sessionStore = {
  subscribe(cb: () => void) {
    sessionListeners.add(cb);
    window.addEventListener("storage", cb);
    return () => {
      sessionListeners.delete(cb);
      window.removeEventListener("storage", cb);
    };
  },
  getSnapshot(): string | null {
    try {
      return window.localStorage.getItem(KEYS.session);
    } catch {
      return null;
    }
  },
  getServerSnapshot: (): string | null => null,
};

export const local = {
  getSession: () => read<Session>(KEYS.session),
  setSession: (s: Session | null) => {
    write(KEYS.session, s);
    sessionListeners.forEach((cb) => cb());
  },

  getFilters: <T>() => read<T>(KEYS.filters),
  setFilters: (f: unknown) => write(KEYS.filters, f),

  getDraft: <T>() => read<T>(KEYS.draft),
  setDraft: (d: unknown) => write(KEYS.draft, d),

  getRecent: () => read<RecentItem[]>(KEYS.recent) ?? [],
  addRecent: (l: Listing) => {
    const item: RecentItem = {
      listing_id: l.listing_id,
      title: l.title,
      price: l.price,
      cover_url: l.cover_url,
    };
    const rest = (read<RecentItem[]>(KEYS.recent) ?? []).filter((r) => r.listing_id !== l.listing_id);
    write(KEYS.recent, [item, ...rest].slice(0, 8));
  },
};

export interface RecentItem {
  listing_id: number;
  title: string;
  price: number;
  cover_url: string | null;
}
