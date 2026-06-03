import { useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { Header } from "./Header.jsx";
import { TrashIcon, CircleIcon, CheckCircleIcon, FolderIcon } from "./icons.jsx";

// basename returns the last path segment, used to label a plan by its origin
// folder. "No Project" (and any non-path value) passes through unchanged.
function basename(p) {
  if (!p) return "No Project";
  const parts = p.split("/").filter(Boolean);
  return parts.length ? parts[parts.length - 1] : p;
}

export function PlanList({ navigate }) {
  const [plans, setPlans] = useState(null);
  const [err, setErr] = useState(null);
  const [statusFilter, setStatusFilter] = useState("active"); // all | active | completed
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
      if (p.status === "completed") await api.reopenPlan(p.id);
      else await api.completePlan(p.id);
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
            </select>
          </label>
          <label className="filter">
            Project
            <select value={projectFilter} onChange={(e) => setProjectFilter(e.target.value)}>
              <option value="all">All</option>
              {projects.map((p) => (
                <option key={p} value={p}>{basename(p)}</option>
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
              return (
              <li
                key={p.id}
                className={`clickable ${completed ? "completed" : ""}`}
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
                  <span className="plan-project" title={p.project || "No Project"}>
                    <FolderIcon />{basename(p.project)}
                  </span>
                </div>
                <span className="badge">v{p.latest_version}</span>
                {p.open_comments > 0 && <span className="badge open">{p.open_comments} open</span>}
                <span className="mono">{p.id}</span>
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
