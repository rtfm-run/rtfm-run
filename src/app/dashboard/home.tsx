"use client";

import Link from "next/link";
import { standingOf } from "@/lib/labels";
import { useApp } from "./app-context";
import QuickstartView from "./quickstart-view";

export default function Home() {
  const app = useApp();
  const first =
    app.quickstarts.find((q) => standingOf(app.lastRunOf(q.id)) === "broken") ?? app.quickstarts[0];

  if (first) return <QuickstartView key={first.id} id={first.id} />;

  return (
    <>
      <header className="report-head">
        <h1>No quickstarts yet</h1>
        <p className="lead">
          {app.repos.length
            ? "Pick a repository and a doc, and the agent will follow it like a new developer."
            : "Start by letting rtfm.run see a repository."}
        </p>
      </header>
      <div className="sheet">
        <p>
          <Link href={app.repos.length ? "/dashboard/new" : "/dashboard/connections"} className="btn">
            {app.repos.length ? "Add your first quickstart" : "Connect GitHub"}
          </Link>
        </p>
      </div>
    </>
  );
}
