import {
  Component,
  Suspense,
  useState,
  useEffect,
  type ErrorInfo,
  type ReactNode,
} from "react";
import Layout from "./components/Layout";
import Dashboard from "./components/Dashboard";
import { lazyRoute } from "./lib/lazyRoute";

const RunDetail = lazyRoute(() => import("./components/RunDetail"));
const CompareView = lazyRoute(() => import("./components/CompareView"));
const TrendsPage = lazyRoute(() => import("./components/TrendsPage"));
const LiveView = lazyRoute(() => import("./components/LiveView"));
const AssurancePage = lazyRoute(() => import("./components/AssurancePage"));
const ReleaseView = lazyRoute(() => import("./components/ReleaseView"));
const DashboardLab = lazyRoute(() => import("./lab/DashboardLab"));

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

const routeComponents = {
  lab: DashboardLab,
  run: RunDetail,
  compare: CompareView,
  trends: TrendsPage,
  live: LiveView,
  assurance: AssurancePage,
  release: ReleaseView,
} as const;

const initialPage = parseHash().page;
if (initialPage !== "home") void routeComponents[initialPage].preload();

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

class RouteErrorBoundary extends Component<
  { children: ReactNode; className?: string },
  { failed: boolean }
> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Unable to load dashboard view:", error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div role="alert" className={this.props.className ?? "text-zinc-100"}>
        <p>This dashboard view could not be loaded. Reload to try again, or return to runs.</p>
        <div className="mt-3 flex gap-4">
          <button
            type="button"
            className="rounded bg-zinc-800 px-3 py-2 text-sm"
            onClick={() => window.location.reload()}
          >
            Reload view
          </button>
          <a href="#/" className="rounded px-3 py-2 text-sm text-blue-400">
            Return to runs
          </a>
        </div>
      </div>
    );
  }
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
      <RouteErrorBoundary
        key={route.demo ? "lab-demo" : "lab"}
        className="p-8 text-zinc-100"
      >
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
      </RouteErrorBoundary>
    );
  }

  return (
    <Layout>
      {route.page === "home" && <Dashboard />}
      {route.page !== "home" && (
        <RouteErrorBoundary
          key={route.page === "run" ? `run/${route.id}` : route.page}
        >
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
        </RouteErrorBoundary>
      )}
    </Layout>
  );
}
