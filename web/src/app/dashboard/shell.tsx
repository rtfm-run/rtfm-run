"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { quickstartTitle, runSummary, STANDING_LABEL, standingOf, type Standing } from "@/lib/labels";
import Wordmark from "../wordmark";
import { useApp } from "./app-context";

type Filter = "all" | Standing;

const FILTERS: Filter[] = ["all", "broken", "unclear", "works", "running"];

export default function Shell({ children }: { children: React.ReactNode }) {
  const app = useApp();
  const pathname = usePathname();
  const router = useRouter();
  const [filter, setFilter] = useState<Filter>("all");
  const signedOut = app.phase === "signed-out";

  useEffect(() => {
    if (signedOut) router.replace("/login");
  }, [signedOut, router]);

  if (app.phase === "loading" || signedOut) {
    return (
      <div className="dash">
        <p className="pad faint">Loading…</p>
      </div>
    );
  }

  if (app.phase === "error") {
    return (
      <div className="gate">
        <Link href="/">
          <Wordmark />
        </Link>
        <h1>The dashboard could not load.</h1>
        <p className="err">{app.error}</p>
        <button type="button" className="btn" onClick={() => app.refresh()}>
          Try again
        </button>
      </div>
    );
  }

  const segments = pathname.split("/");
  const section = segments[2];
  const routeId = Number(segments[3]);
  const items = app.quickstarts.map((q) => {
    const last = app.lastRunOf(q.id);
    return { q, last, standing: standingOf(last) };
  });
  const firstTroubled = items.find((i) => i.standing === "broken") ?? items[0];
  const activeQuickstart =
    section === "quickstarts"
      ? routeId
      : section === "runs"
        ? app.runs.find((r) => r.id === routeId)?.quickstart_id
        : section
          ? undefined
          : firstTroubled?.q.id;

  const count = (f: Filter) => (f === "all" ? items.length : items.filter((i) => i.standing === f).length);
  const visible = filter === "all" ? items : items.filter((i) => i.standing === filter);

  async function signOut() {
    await app.api.logout();
    router.replace("/login");
  }

  return (
    <div className="dash" data-view={section ? "page" : "list"}>
      <aside className="side">
        <div className="brand">
          <Link href="/">
            <Wordmark />
          </Link>
        </div>

        <Link href="/dashboard/new" className={`new${section === "new" ? " on" : ""}`}>
          + new quickstart
        </Link>

        <div className="side-scroll">
          <div className="side-title">Quickstarts</div>
          <div className="filters" role="group" aria-label="Filter by status">
            {FILTERS.map((f) => (
              <button
                key={f}
                type="button"
                className={filter === f ? "on" : undefined}
                aria-pressed={filter === f}
                onClick={() => setFilter(f)}
              >
                {f} ({count(f)})
              </button>
            ))}
          </div>
          <nav className="items" aria-label="Quickstarts">
            {visible.map(({ q, last, standing }) => (
              <Link
                key={q.id}
                href={`/dashboard/quickstarts/${q.id}`}
                className={`item${q.id === activeQuickstart ? " active" : ""}`}
                aria-current={q.id === activeQuickstart ? "page" : undefined}
              >
                <span className="item-top">
                  <span className="target">{quickstartTitle(q, app.repoOf(q.repo_id))}</span>
                  <span className="dots" aria-hidden="true" />
                  <span className={`st s-${standing}`}>{STANDING_LABEL[standing]}</span>
                </span>
                <span className="item-sum">{q.name || runSummary(last)}</span>
              </Link>
            ))}
            {items.length === 0 && <p className="faint none">None yet.</p>}
            {items.length > 0 && visible.length === 0 && <p className="faint none">None.</p>}
          </nav>
        </div>

        <div className="side-foot">
          <Link href="/dashboard/connections" className={section === "connections" ? "on" : undefined}>
            connections
          </Link>
          {app.api.sample ? (
            <span className="faint">sample data</span>
          ) : (
            <span>
              <span className="faint">{app.user?.login} </span>
              <button type="button" className="act" onClick={signOut}>
                [sign out]
              </button>
            </span>
          )}
        </div>
      </aside>

      <main className="main">
        {app.error && <p className="err banner">{app.error}</p>}
        {children}
      </main>
    </div>
  );
}
