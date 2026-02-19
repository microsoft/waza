import type { ReactNode } from "react";
import { Activity, GitCompareArrows, TrendingUp, Radio } from "lucide-react";

const navItems = [
  { href: "#/", label: "Runs" },
  { href: "#/compare", label: "Compare", icon: GitCompareArrows },
  { href: "#/trends", label: "Trends", icon: TrendingUp },
  { href: "#/live", label: "Live", icon: Radio },
];

export default function Layout({ children }: { children: ReactNode }) {
  const hash = typeof window !== "undefined" ? window.location.hash : "#/";

  return (
    <div className="min-h-screen bg-zinc-900">
      <header className="border-b border-zinc-800 px-6 py-4">
        <div className="flex items-center gap-6">
          <a href="#/" className="flex items-center gap-2 text-zinc-100">
            <Activity className="h-5 w-5 text-blue-500" />
            <span className="text-lg font-semibold tracking-tight">waza</span>
            <span className="text-sm text-zinc-500">eval dashboard</span>
          </a>
          <nav className="flex items-center gap-1">
            {navItems.map((item) => {
              const active =
                item.href === "#/"
                  ? hash === "#/" || hash === "" || hash === "#"
                  : hash === item.href;
              return (
                <a
                  key={item.href}
                  href={item.href}
                  className={`flex items-center gap-1.5 rounded px-3 py-1.5 text-sm transition-colors ${
                    active
                      ? "bg-zinc-800 text-zinc-100"
                      : "text-zinc-400 hover:text-zinc-200"
                  }`}
                >
                  {item.icon && <item.icon className="h-3.5 w-3.5" />}
                  {item.label}
                </a>
              );
            })}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-6 py-8">{children}</main>
    </div>
  );
}
