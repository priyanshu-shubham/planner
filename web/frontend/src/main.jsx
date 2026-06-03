import { createRoot } from "react-dom/client";
import { useEffect, useState, useCallback } from "react";
import { api } from "./api.js";
import { PlanList } from "./PlanList.jsx";
import { VersionPage } from "./VersionPage.jsx";

// Minimal client-side router: tracks location.pathname and exposes navigate().
export function useRouter() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  const navigate = useCallback((to, { replace = false } = {}) => {
    if (replace) window.history.replaceState({}, "", to);
    else window.history.pushState({}, "", to);
    setPath(to);
  }, []);
  return { path, navigate };
}

function App() {
  const { path, navigate } = useRouter();

  // /plans/:id/v/:n
  let m = path.match(/^\/plans\/([^/]+)\/v\/(\d+)\/?$/);
  if (m) return <VersionPage planId={m[1]} number={Number(m[2])} navigate={navigate} />;

  // /plans/:id  -> redirect to latest version
  m = path.match(/^\/plans\/([^/]+)\/?$/);
  if (m) return <PlanRedirect planId={m[1]} navigate={navigate} />;

  // /
  return <PlanList navigate={navigate} />;
}

function PlanRedirect({ planId, navigate }) {
  const [err, setErr] = useState(null);
  useEffect(() => {
    api
      .planMeta(planId)
      .then((meta) => navigate(`/plans/${planId}/v/${meta.latest}`, { replace: true }))
      .catch((e) => setErr(e.message));
  }, [planId]);
  return <div className="wrap">{err ? <p className="error">{err}</p> : "Loading…"}</div>;
}

createRoot(document.getElementById("root")).render(<App />);
