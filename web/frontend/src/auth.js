// Auth state for the SPA. In no-auth mode (the default server) this stays
// disabled and the app behaves exactly as before. In authed mode it holds the
// signed-in user and a short-lived access token kept in memory only; the durable
// session lives in the httpOnly refresh cookie, replayed via refresh().
const state = { enabled: false, user: null, accessToken: null };

export const auth = {
  get enabled() {
    return state.enabled;
  },
  get user() {
    return state.user;
  },
  get accessToken() {
    return state.accessToken;
  },

  // init reads the server's auth mode, then (when authed) makes one refresh
  // attempt so a returning user with a live cookie lands signed in.
  async init() {
    try {
      const cfg = await fetch("/api/config").then((r) => r.json());
      state.enabled = cfg.auth === "google";
    } catch (_) {
      state.enabled = false;
    }
    if (state.enabled) await this.refresh();
    return { enabled: state.enabled, user: state.user };
  },

  // refresh rotates the refresh cookie and stores a fresh access token + user.
  // Returns true on success; clears state and returns false otherwise.
  async refresh() {
    try {
      const r = await fetch("/api/auth/refresh", { method: "POST" });
      if (!r.ok) {
        state.user = null;
        state.accessToken = null;
        return false;
      }
      const j = await r.json();
      state.accessToken = j.access_token;
      state.user = j.user;
      return true;
    } catch (_) {
      state.user = null;
      state.accessToken = null;
      return false;
    }
  },

  async logout() {
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } catch (_) {}
    state.user = null;
    state.accessToken = null;
  },
};
