import { auth } from "./auth.js";

// Thin wrapper over the planner JSON API. Every call returns parsed JSON or
// throws an Error carrying the server's message. In authed mode it attaches the
// access token and, on a 401, transparently refreshes once and retries.
async function req(method, path, body, retried = false) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  if (auth.enabled && auth.accessToken) {
    opts.headers["Authorization"] = `Bearer ${auth.accessToken}`;
  }
  const resp = await fetch(path, opts);
  if (resp.status === 401 && auth.enabled && !retried) {
    if (await auth.refresh()) return req(method, path, body, true);
  }
  if (!resp.ok) {
    let msg = `request failed (${resp.status})`;
    try {
      const j = await resp.json();
      if (j.error) msg = j.error;
    } catch (_) {}
    throw new Error(msg);
  }
  if (resp.status === 204) return null;
  return resp.json();
}

export const api = {
  listPlans: () => req("GET", "/api/plans"),
  planMeta: (id) => req("GET", `/api/plans/${id}`),
  deletePlan: (id) => req("DELETE", `/api/plans/${id}`),
  setPlanStatus: (id, status) => req("POST", `/api/plans/${id}/status`, { status }),
  setPlanProject: (id, project) => req("POST", `/api/plans/${id}/project`, { project }),
  versionView: (id, n) => req("GET", `/api/plans/${id}/v/${n}`),
  // referenced-file content by sha (content-addressed, immutably cached)
  file: (sha) => req("GET", `/api/files/${sha}`),
  addComment: (id, n, c) => req("POST", `/api/plans/${id}/v/${n}/comments`, c),
  // comment actions are addressed under the plan (or share) id — the id in the
  // path is the access credential the server authorizes against
  resolveComment: (id, cid) => req("POST", `/api/plans/${id}/comments/${cid}/resolve`),
  reopenComment: (id, cid) => req("POST", `/api/plans/${id}/comments/${cid}/reopen`),
  keepComment: (id, cid) => req("POST", `/api/plans/${id}/comments/${cid}/keep`),
  deleteComment: (id, cid) => req("DELETE", `/api/plans/${id}/comments/${cid}`),
  // human reply (the agent replies via the CLI, author="agent")
  addReply: (id, cid, body) => req("POST", `/api/plans/${id}/comments/${cid}/replies`, { author: "human", body }),
  deleteReply: (id, cid, rid) => req("DELETE", `/api/plans/${id}/comments/${cid}/replies/${rid}`),
  // share links: create-or-get the plan's share id / revoke it (owner only)
  createShare: (id, policy) => req("POST", `/api/plans/${id}/share`, policy),
  revokeShare: (id) => req("DELETE", `/api/plans/${id}/share`),
  // personal access tokens (authed mode; web-session only)
  listPats: () => req("GET", "/api/pats"),
  createPat: (name) => req("POST", "/api/pats", { name }),
  deletePat: (id) => req("DELETE", `/api/pats/${id}`),
};
