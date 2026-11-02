import { lazy, Suspense, useState, useEffect } from "react";
import Layout from "./components/Layout";
import Dashboard from "./components/Dashboard";

const RunDetail = lazy(() => import("./components/RunDetail"));
const CompareView = lazy(() => import("./components/CompareView"));
const TrendsPage = lazy(() => import("./components/TrendsPage"));
const LiveView = lazy(() => import("./components/LiveView"));
const AssurancePage = lazy(() => import("./components/AssurancePage"));
const ReleaseView = lazy(() => import("./components/ReleaseView"));
const DashboardLab = lazy(() => import("./lab/DashboardLab"));

type Route =
  | { page: "home" }
  | { page: "lab"; demo: boolean }
  | { page: "run"; id: string }
  | { page: "compare" }
  | { page: "trends" }
  | { page: "live" }
  | { page: "assurance" }
  | { page: "release" };

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
  if (hash === "/assurance") return { page: "assurance" };
  if (hash === "/release") return { page: "release" };
  const runMatch = hash.match(/^\/runs\/(.+)$/);
  if (runMatch?.[1]) return { page: "run", id: runMatch[1] };
  return { page: "home" };
}

function LoadingRoute({
  label,
  className = "text-zinc-400",
}: {
  label: string;
  className?: string;
}) {
  return (
    <p role="status" className={className}>
      Loading {label}...
    </p>
  );
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
        fallback={
          <LoadingRoute
            label="dashboard lab"
            className="p-8 text-zinc-100"
          />
        }
      >
        <DashboardLab key={route.demo ? "demo" : "results"} demo={route.demo} />
      </Suspense>
    );
  }

  return (
    <Layout>
      {route.page === "home" && <Dashboard />}
      {route.page !== "home" && (
        <Suspense
          fallback={
            <LoadingRoute
              label={
                {
                  run: "run details",
                  compare: "run comparison",
                  trends: "trends",
                  live: "live view",
                  assurance: "report inspector",
                  release: "release policies",
                }[route.page]
              }
            />
          }
        >
          {route.page === "run" && <RunDetail id={route.id} />}
          {route.page === "compare" && <CompareView />}
          {route.page === "trends" && <TrendsPage />}
          {route.page === "live" && <LiveView />}
          {route.page === "assurance" && <AssurancePage />}
          {route.page === "release" && <ReleaseView />}
        </Suspense>
      )}
    </Layout>
  );
}
