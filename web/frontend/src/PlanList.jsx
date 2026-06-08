import { useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { Header } from "./Header.jsx";
import { TrashIcon, CircleIcon, CheckCircleIcon, FolderIcon, GitBranchIcon, ArchiveBoxIcon, UnarchiveIcon } from "./icons.jsx";

// basename returns the last path segment, used to label a plan by its project —
// a folder's name (/home/me/planner -> planner) or a repo identity's name
// (github.com/me/planner -> planner). "No Project" passes through unchanged.
function basename(p) {
  if (!p) return "No Project";
  const parts = p.split("/").filter(Boolean);
  return parts.length ? parts[parts.length - 1] : p;
}

// isRepoId distinguishes the two project value shapes: a git remote identity
// (host/owner/repo) vs. a filesystem path (starts with "/"). Drives which icon
// labels the plan. "No Project" is neither.
function isRepoId(p) {
  return !!p && p !== "No Project" && !p.startsWith("/");
}

export function PlanList({ navigate }) {
  const [plans, setPlans] = useState(null);
  const [err, setErr] = useState(null);
  const [statusFilter, setStatusFilter] = useState("active"); // all | active | completed | stashed
  const [projectFilter, setProjectFilter] = useState("all"); // all | <project path>

  function load() {
    api.listPlans().then(setPlans).catch((e) => setErr(e.message));
  }
  useEffect(() => { load(); }, []);

  async function deletePlan(p) {
    if (!confirm(`Delete plan “${p.title}” and all its versions and comments? This cannot be undone.`)) return;
    try {
      await api.deletePlan(p.id);
      load();
    } catch (e) {
      setErr(e.message);
    }
  }

  async function toggleComplete(p) {
    try {
      await api.setPlanStatus(p.id, p.status === "completed" ? "active" : "completed");
      load();
    } catch (e) {
      setErr(e.message);
    }
  }

  async function toggleStash(p) {
    try {
      await api.setPlanStatus(p.id, p.status === "stashed" ? "active" : "stashed");
      load();
    } catch (e) {
      setErr(e.message);
    }
  }

  async function editProject(p) {
    const next = prompt("Project for this plan (a folder path or repo identity; blank clears it):", p.project || "");
    if (next === null) return; // cancelled
    if (next.trim() === (p.project || "")) return; // unchanged
    try {
      await api.setPlanProject(p.id, next.trim());
      load();
    } catch (e) {
      setErr(e.message);
    }
  }

  // Distinct projects for the project filter dropdown, sorted by display name.
  const projects = useMemo(() => {
    const set = new Set((plans || []).map((p) => p.project || "No Project"));
    return [...set].sort((a, b) => basename(a).localeCompare(basename(b)));
  }, [plans]);

  // When two distinct project values share a basename (e.g. a folder path and a
  // git identity for the same repo), label them with their full value so the
  // dropdown entries are tellable apart instead of showing the name twice.
  const projectLabels = useMemo(() => {
    const counts = {};
    for (const p of projects) counts[basename(p)] = (counts[basename(p)] || 0) + 1;
    return Object.fromEntries(projects.map((p) => [p, counts[basename(p)] > 1 ? p : basename(p)]));
  }, [projects]);

  const shown = (plans || []).filter((p) => {
    if (statusFilter !== "all" && (p.status || "active") !== statusFilter) return false;
    if (projectFilter !== "all" && (p.project || "No Project") !== projectFilter) return false;
    return true;
  });

  return (
    <>
      <Header navigate={navigate} />
      <main className="wrap">
        <div className="toolbar">
          <strong>Plans</strong>
          <span className="spacer" />
          <label className="filter">
            Status
            <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              <option value="all">All</option>
              <option value="active">Active</option>
              <option value="completed">Completed</option>
              <option value="stashed">Stashed</option>
            </select>
          </label>
          <label className="filter">
            Project
            <select value={projectFilter} onChange={(e) => setProjectFilter(e.target.value)}>
              <option value="all">All</option>
              {projects.map((p) => (
                <option key={p} value={p}>{projectLabels[p]}</option>
              ))}
            </select>
          </label>
        </div>
        {err && <p className="error">{err}</p>}
        {plans && plans.length === 0 && (
          <p className="empty">No plans yet. An agent can create one with <code>planner create</code>.</p>
        )}
        {plans && plans.length > 0 && shown.length === 0 && (
          <p className="empty">No plans match the current filters.</p>
        )}
        {shown.length > 0 && (
          <ul className="plan-list">
            {shown.map((p) => {
              const completed = p.status === "completed";
              const stashed = p.status === "stashed";
              return (
              <li
                key={p.id}
                className={`clickable ${completed ? "completed" : ""} ${stashed ? "stashed" : ""}`}
                onClick={() => navigate(`/plans/${p.id}`)}
              >
                <button
                  className={`complete-toggle ${completed ? "done" : ""}`}
                  title={completed ? "Reopen plan" : "Mark plan completed"}
                  aria-label={completed ? "Reopen plan" : "Mark plan completed"}
                  aria-pressed={completed}
                  onClick={(e) => { e.stopPropagation(); toggleComplete(p); }}
                >
                  {completed ? <CheckCircleIcon /> : <CircleIcon />}
                </button>
                <div className="plan-main">
                  <a className="title" href={`/plans/${p.id}`} onClick={(e) => { e.preventDefault(); e.stopPropagation(); navigate(`/plans/${p.id}`); }}>
                    {p.title}
                  </a>
                  <button
                    type="button"
                    className="plan-project"
                    title={`${p.project || "No Project"} — click to change project`}
                    onClick={(e) => { e.stopPropagation(); editProject(p); }}
                  >
                    {isRepoId(p.project) ? <GitBranchIcon /> : <FolderIcon />}<span>{basename(p.project)}</span>
                  </button>
                </div>
                {p.open_comments > 0 && (
                  <span className="badge open" title={`${p.open_comments} open comment${p.open_comments === 1 ? "" : "s"}`}>
                    {p.open_comments}
                  </span>
                )}
                <span className="badge">v{p.latest_version}</span>
                <span className="mono">{p.id}</span>
                {p.status === "active" && (
                  <button
                    className="icon-btn"
                    title="Stash plan"
                    aria-label="Stash plan"
                    onClick={(e) => { e.stopPropagation(); toggleStash(p); }}
                  >
                    <ArchiveBoxIcon />
                  </button>
                )}
                {stashed && (
                  <button
                    className="icon-btn"
                    title="Unstash plan"
                    aria-label="Unstash plan"
                    onClick={(e) => { e.stopPropagation(); toggleStash(p); }}
                  >
                    <UnarchiveIcon />
                  </button>
                )}
                <button
                  className="icon-btn danger"
                  title="Delete plan"
                  aria-label="Delete plan"
                  onClick={(e) => { e.stopPropagation(); deletePlan(p); }}
                >
                  <TrashIcon />
                </button>
              </li>
              );
            })}
          </ul>
        )}
      </main>
    </>
  );
}
