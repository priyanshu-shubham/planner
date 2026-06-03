// Thin wrapper over the planner JSON API. Every call returns parsed JSON or
// throws an Error carrying the server's message.
async function req(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
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
  completePlan: (id) => req("POST", `/api/plans/${id}/complete`),
  reopenPlan: (id) => req("POST", `/api/plans/${id}/reopen`),
  versionView: (id, n) => req("GET", `/api/plans/${id}/v/${n}`),
  addComment: (id, n, c) => req("POST", `/api/plans/${id}/v/${n}/comments`, c),
  resolveComment: (cid) => req("POST", `/api/comments/${cid}/resolve`),
  reopenComment: (cid) => req("POST", `/api/comments/${cid}/reopen`),
  keepComment: (cid) => req("POST", `/api/comments/${cid}/keep`),
  deleteComment: (cid) => req("DELETE", `/api/comments/${cid}`),
  // human reply (the agent replies via the CLI, author="agent")
  addReply: (cid, body) => req("POST", `/api/comments/${cid}/replies`, { author: "human", body }),
};
