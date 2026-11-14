"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";
import { useAction, useApp } from "../app-context";

export default function NewQuickstart() {
  const app = useApp();
  const router = useRouter();
  const wanted = Number(useSearchParams().get("repo"));
  const create = useAction();

  const [picked, setPicked] = useState<number>();
  const [docPath, setDocPath] = useState("README.md");
  const [goal, setGoal] = useState("");
  const [successCheck, setSuccessCheck] = useState("");

  const repoId = picked ?? app.repos.find((r) => r.id === wanted)?.id ?? app.repos[0]?.id;

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!repoId) return;
    create.run(async () => {
      const quickstart = await app.api.createQuickstart(repoId, {
        doc_path: docPath.trim() || "README.md",
        goal: goal.trim(),
        success_check: successCheck.trim(),
      });
      const run = await app.api.createRun(quickstart.id);
      await app.refresh();
      router.push(`/dashboard/runs/${run.id}`);
    });
  }

  return (
    <>
      <Link href="/dashboard" className="back">
        ← Dashboard
      </Link>
      <header className="report-head">
        <h1>New quickstart</h1>
        <p className="lead">Point the agent at a doc and tell it what a new developer is trying to do.</p>
      </header>

      {app.repos.length === 0 ? (
        <div className="sheet">
          <p>vanguard.run cannot see any of your repositories yet.</p>
          <p>
            <Link href="/dashboard/connections" className="btn">
              Connect GitHub
            </Link>
          </p>
        </div>
      ) : (
        <form className="sheet form" onSubmit={submit}>
          <div className="field">
            <span className="field-name">Repository</span>
            <div className="pick" role="radiogroup" aria-label="Repository">
              {app.repos.map((r) => (
                <button
                  key={r.id}
                  type="button"
                  role="radio"
                  aria-checked={repoId === r.id}
                  className={`pick-row${repoId === r.id ? " on" : ""}`}
                  onClick={() => setPicked(r.id)}
                >
                  {r.full_name}
                </button>
              ))}
            </div>
          </div>

          <label className="field">
            <span className="field-name">Doc to follow</span>
            <input value={docPath} onChange={(e) => setDocPath(e.target.value)} placeholder="README.md" />
          </label>

          <label className="field">
            <span className="field-name">Goal</span>
            <input
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="start the server and see it respond"
              required
            />
          </label>

          <label className="field">
            <span className="field-name">Success check</span>
            <input
              value={successCheck}
              onChange={(e) => setSuccessCheck(e.target.value)}
              placeholder="optional: a command that exits 0 when the goal is met"
            />
          </label>

          <div className="field">
            <span className="field-name" />
            <button type="submit" className="btn" disabled={create.busy || !repoId || !goal.trim()}>
              {create.busy ? "Starting…" : "Create and run"}
            </button>
          </div>
          {create.error && <p className="err">{create.error}</p>}
        </form>
      )}
    </>
  );
}
