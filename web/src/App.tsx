import { lazy, Suspense, useState, useEffect } from "react";
import Layout from "./components/Layout";
import Dashboard from "./components/Dashboard";
import RunDetail from "./components/RunDetail";
import CompareView from "./components/CompareView";
import TrendsPage from "./components/TrendsPage";
import LiveView from "./components/LiveView";

const DashboardLab = lazy(() => import("./lab/DashboardLab"));

type Route =
  | { page: "home" }
  | { page: "lab"; demo: boolean }
  | { page: "run"; id: string }
  | { page: "compare" }
  | { page: "trends" }
  | { page: "live" };

function parseHash(): Route {
  const hash = window.location.hash.slice(1);
  if (hash === "/lab" || hash.startsWith("/lab/")) {
    return {
      page: "lab",
      demo: hash === "/lab/demo" || hash.startsWith("/lab/demo/"),
    };
  }
  if (hash === "/compare") return { page: "compare" };
  if (hash === "/trends") return { page: "trends" };
  if (hash === "/live") return { page: "live" };
  const runMatch = hash.match(/^\/runs\/(.+)$/);
  if (runMatch?.[1]) return { page: "run", id: runMatch[1] };
  return { page: "home" };
}

export default function App() {
  const [route, setRoute] = useState<Route>(parseHash);

  useEffect(() => {
    const onHashChange = () => setRoute(parseHash());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  if (route.page === "lab") {
    return (
      <Suspense
        fallback={<p className="p-8 text-zinc-100">Loading dashboard lab...</p>}
      >
        <DashboardLab key={route.demo ? "demo" : "results"} demo={route.demo} />
      </Suspense>
    );
  }

  return (
    <Layout>
      {route.page === "home" && <Dashboard />}
      {route.page === "run" && <RunDetail id={route.id} />}
      {route.page === "compare" && <CompareView />}
      {route.page === "trends" && <TrendsPage />}
      {route.page === "live" && <LiveView />}
    </Layout>
  );
}
