import { createRoot } from "react-dom/client";
import { useEffect, useState, useCallback } from "react";
import { api } from "./api.js";
import { auth } from "./auth.js";
import { PlanList } from "./PlanList.jsx";
import { VersionPage } from "./VersionPage.jsx";
import { VersionCompare } from "./VersionCompare.jsx";
import { Login } from "./Login.jsx";
import { CliSetup } from "./CliSetup.jsx";
import { CliAccess } from "./CliAccess.jsx";

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
    setPath(to.split("?")[0]);
  }, []);
  return { path, navigate };
}

// route renders the page for a path (used once the user is past the auth gate).
function route(path, navigate) {
  let m = path.match(/^\/plans\/([^/]+)\/v\/(\d+)\/?$/);
  if (m) return <VersionPage planId={m[1]} number={Number(m[2])} navigate={navigate} />;

  m = path.match(/^\/plans\/([^/]+)\/compare\/(\d+)\.\.\.(\d+)\/?$/);
  if (m) return <VersionCompare planId={m[1]} from={Number(m[2])} to={Number(m[3])} navigate={navigate} />;

  m = path.match(/^\/plans\/([^/]+)\/?$/);
  if (m) return <PlanRedirect planId={m[1]} navigate={navigate} />;

  if (path === "/cli-setup") return <CliSetup navigate={navigate} />;
  if (path === "/settings/cli") return <CliAccess navigate={navigate} />;

  return <PlanList navigate={navigate} />;
}

function App() {
  const { path, navigate } = useRouter();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    auth.init().then(() => setReady(true));
  }, []);

  if (!ready) return <div className="wrap">Loading…</div>;

  // Authed mode, not signed in: show the login screen, preserving the intended
  // destination (path + query) so the OAuth round-trip returns the user there.
  if (auth.enabled && !auth.user) {
    const here = window.location.pathname + window.location.search;
    return <Login navigate={navigate} next={path === "/login" ? "/" : here} />;
  }

  // Signed in (or auth disabled): /login has nothing to show — go home.
  if (path === "/login") return <Redirect to="/" navigate={navigate} />;

  return route(path, navigate);
}

// Redirect performs a replace-navigation in an effect (not during render).
function Redirect({ to, navigate }) {
  useEffect(() => { navigate(to, { replace: true }); }, [to]);
  return <div className="wrap">Loading…</div>;
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
