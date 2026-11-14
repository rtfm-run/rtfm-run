"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useState } from "react";
import type { Quickstart, Trigger } from "@/lib/api-types";
import {
  failureLabel,
  quickstartTitle,
  runSummary,
  shortSha,
  STANDING_LABEL,
  standingOf,
  STEP_LABEL,
  took,
  when,
} from "@/lib/labels";
import { useAction, useApp, useLoad } from "./app-context";

function splitList(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function Settings({ quickstart, onSaved }: { quickstart: Quickstart; onSaved: () => void }) {
  const app = useApp();
  const router = useRouter();
  const save = useAction();
  const remove = useAction();
  const [form, setForm] = useState({
    name: quickstart.name,
    doc_path: quickstart.doc_path,
    goal: quickstart.goal,
    success_check: quickstart.success_check,
    branch: quickstart.branch,
    fix: quickstart.fix,
    verify: quickstart.verify,
  });

  const text = (key: "name" | "doc_path" | "goal" | "success_check" | "branch", label: string, hint: string) => (
    <label className="field">
      <span className="field-name">{label}</span>
      <input value={form[key]} placeholder={hint} onChange={(e) => setForm({ ...form, [key]: e.target.value })} />
    </label>
  );

  function submit(e: React.FormEvent) {
    e.preventDefault();
    save.run(async () => {
      await app.api.updateQuickstart(quickstart.id, form);
      await app.refresh();
      onSaved();
    });
  }

  function destroy() {
    if (!window.confirm("Delete this quickstart and all of its runs?")) return;
    remove.run(async () => {
      await app.api.deleteQuickstart(quickstart.id);
      await app.refresh();
      router.push("/dashboard");
    });
  }

  return (
    <form className="form" onSubmit={submit}>
      {text("name", "Name", "optional label")}
      {text("doc_path", "Doc to follow", "README.md")}
      {text("goal", "Goal", "what a new developer should achieve")}
      {text("success_check", "Success check", "optional shell command that exits 0 on success")}
      {text("branch", "Branch", "default branch")}
      <div className="field">
        <span className="field-name">After a failure</span>
        <div className="choices">
          <button
            type="button"
            className={`check${form.fix ? " on" : ""}`}
            aria-pressed={form.fix}
            onClick={() => setForm({ ...form, fix: !form.fix })}
          >
            write a fix
          </button>
          <button
            type="button"
            className={`check${form.verify ? " on" : ""}`}
            aria-pressed={form.verify}
            onClick={() => setForm({ ...form, verify: !form.verify })}
          >
            rerun to verify it
          </button>
        </div>
      </div>
      <div className="field">
        <span className="field-name" />
        <div className="actions">
          <button type="submit" className="btn" disabled={save.busy}>
            {save.busy ? "Saving…" : "Save"}
          </button>
          <button type="button" className="act danger" disabled={remove.busy} onClick={destroy}>
            [delete quickstart]
          </button>
        </div>
      </div>
      {(save.error || remove.error) && <p className="err">{save.error ?? remove.error}</p>}
    </form>
  );
}

function Credentials({ quickstartId }: { quickstartId: number }) {
  const app = useApp();
  const action = useAction();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const load = useCallback(() => app.api.credentials(quickstartId), [app.api, quickstartId]);
  const { data: names = [], reload } = useLoad(load);

  function add(e: React.FormEvent) {
    e.preventDefault();
    action.run(async () => {
      await app.api.setCredential(quickstartId, name.trim(), value);
      setName("");
      setValue("");
      reload();
    });
  }

  return (
    <>
      <p className="faint">
        Secrets the quickstart needs, such as an API key. The agent can use them; nobody can read them back.
      </p>
      <ul className="plain">
        {names.map((n) => (
          <li key={n}>
            <span>{n}</span>
            <button
              type="button"
              className="act"
              onClick={() =>
                action.run(async () => {
                  await app.api.deleteCredential(quickstartId, n);
                  reload();
                })
              }
            >
              [remove]
            </button>
          </li>
        ))}
        {names.length === 0 && <li className="faint">None stored.</li>}
      </ul>
      <form className="inline-form" onSubmit={add}>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="GEMINI_API_KEY" aria-label="Name" />
        <input
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="value"
          type="password"
          aria-label="Value"
          autoComplete="off"
        />
        <button type="submit" className="btn" disabled={action.busy || !name.trim() || !value}>
          Add
        </button>
      </form>
      {action.error && <p className="err">{action.error}</p>}
    </>
  );
}

function Triggers({ quickstartId, triggers, onChange }: { quickstartId: number; triggers: Trigger[]; onChange: () => void }) {
  const app = useApp();
  const action = useAction();
  const [branches, setBranches] = useState("");
  const [paths, setPaths] = useState("");

  function add(e: React.FormEvent) {
    e.preventDefault();
    action.run(async () => {
      await app.api.createTrigger(quickstartId, { kind: "push", branches: splitList(branches), paths: splitList(paths) });
      setBranches("");
      setPaths("");
      onChange();
    });
  }

  return (
    <>
      <p className="faint">Rerun this quickstart automatically when someone pushes to the repo.</p>
      <ul className="plain">
        {triggers.map((t) => (
          <li key={t.id}>
            <span>
              On push to {t.branches.length ? t.branches.join(", ") : "the quickstart’s branch"}
              {t.paths.length ? `, when ${t.paths.join(", ")} changes` : ""}
              {t.enabled ? "" : " (paused)"}
            </span>
            <span className="actions">
              <button
                type="button"
                className="act"
                onClick={() =>
                  action.run(async () => {
                    await app.api.updateTrigger(t.id, { enabled: !t.enabled });
                    onChange();
                  })
                }
              >
                [{t.enabled ? "pause" : "resume"}]
              </button>
              <button
                type="button"
                className="act"
                onClick={() =>
                  action.run(async () => {
                    await app.api.deleteTrigger(t.id);
                    onChange();
                  })
                }
              >
                [remove]
              </button>
            </span>
          </li>
        ))}
        {triggers.length === 0 && <li className="faint">No triggers. Runs start only when you press Run now.</li>}
      </ul>
      <form className="inline-form" onSubmit={add}>
        <input
          value={branches}
          onChange={(e) => setBranches(e.target.value)}
          placeholder="branches, e.g. main, release/*"
          aria-label="Branches"
        />
        <input
          value={paths}
          onChange={(e) => setPaths(e.target.value)}
          placeholder="paths, e.g. README.md, docs/**"
          aria-label="Paths"
        />
        <button type="submit" className="btn" disabled={action.busy}>
          Add push trigger
        </button>
      </form>
      {action.error && <p className="err">{action.error}</p>}
    </>
  );
}

export default function QuickstartView({ id }: { id: number }) {
  const app = useApp();
  const router = useRouter();
  const start = useAction();

  const loadDetail = useCallback(() => app.api.quickstart(id), [app.api, id]);
  const loadSteps = useCallback(() => app.api.stepHealth(id), [app.api, id]);
  const detail = useLoad(loadDetail);
  const steps = useLoad(loadSteps);

  if (detail.loading && !detail.data) return <p className="faint">Loading…</p>;
  if (!detail.data) return <p className="err">{detail.error ?? "Quickstart not found."}</p>;

  const { quickstart, triggers } = detail.data;
  const repo = app.repoOf(quickstart.repo_id);
  const runs = app.runs.filter((r) => r.quickstart_id === id);
  const last = runs[0] ?? detail.data.last_run ?? undefined;
  const standing = standingOf(last);
  const health = steps.data ?? [];

  function runNow() {
    start.run(async () => {
      const run = await app.api.createRun(id);
      await app.refresh();
      router.push(`/dashboard/runs/${run.id}`);
    });
  }

  return (
    <>
      <Link href="/dashboard" className="back">
        ← All quickstarts
      </Link>

      <header className="report-head">
        <div className="title-row">
          <h1>{quickstartTitle(quickstart, repo)}</h1>
          <span className={`st big s-${standing}`}>{STANDING_LABEL[standing]}</span>
          <button type="button" className="btn push" disabled={start.busy || standing === "running"} onClick={runNow}>
            {start.busy ? "Starting…" : standing === "running" ? "Running…" : "Run now"}
          </button>
        </div>
        <p className="lead">
          {runSummary(last)}.{" "}
          {last && (
            <Link href={`/dashboard/runs/${last.id}`} className="ul">
              Open the latest run
            </Link>
          )}
        </p>
        {start.error && <p className="err">{start.error}</p>}
      </header>

      <div className="report">
        <div>
          <section className="block">
            <h2>Steps</h2>
            {health.length === 0 ? (
              <p className="faint">
                {steps.loading ? "Loading…" : "No completed run yet. Press Run now and the agent will walk the doc."}
              </p>
            ) : (
              <ol className="steps">
                {health.map((s) => (
                  <li key={s.step_key} className={`step v-${s.status}`}>
                    <span className="num">{s.index}.</span>
                    <div>
                      <div className="step-head">
                        <h3>{s.instruction || s.doc_command}</h3>
                        <span className={`st v-${s.status}`}>{STEP_LABEL[s.status]}</span>
                      </div>
                      {s.doc_command && (
                        <div className="cmd">
                          <span className="label">The docs say</span>
                          <code>{s.doc_command}</code>
                        </div>
                      )}
                      {(s.status === "fail" || s.status === "unsupported") && (
                        <div className="card">
                          <p>
                            {failureLabel(s.failure) && <b>{failureLabel(s.failure)}. </b>}
                            {s.diagnosis}
                          </p>
                        </div>
                      )}
                      <p className="faint verified">
                        {s.last_verified_at
                          ? `Last verified ${when(s.last_verified_at)} at ${shortSha(s.last_verified_commit ?? "")}`
                          : "Never verified as written"}
                      </p>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </section>

          <section className="block">
            <h2>Runs</h2>
            {runs.length === 0 ? (
              <p className="faint">No runs yet.</p>
            ) : (
              <div className="runs">
                {runs.map((r) => {
                  const s = standingOf(r);
                  return (
                    <Link key={r.id} href={`/dashboard/runs/${r.id}`} className="run-row">
                      <span>#{r.id}</span>
                      <span className={`st s-${s}`}>{STANDING_LABEL[s]}</span>
                      <span className="faint">{r.trigger}</span>
                      <span className="faint">{r.commit_sha ? shortSha(r.commit_sha) : r.ref}</span>
                      <span className="faint">{took(r)}</span>
                      <span className="faint end">{when(r.started_at ?? r.queued_at)}</span>
                    </Link>
                  );
                })}
              </div>
            )}
          </section>

          <section className="block">
            <h2>Settings</h2>
            <Settings key={quickstart.updated_at} quickstart={quickstart} onSaved={detail.reload} />
          </section>

          <section className="block" id="credentials">
            <h2>Credentials</h2>
            <Credentials quickstartId={id} />
          </section>

          <section className="block">
            <h2>Triggers</h2>
            <Triggers quickstartId={id} triggers={triggers} onChange={detail.reload} />
          </section>
        </div>

        <aside className="rail">
          <h2>Details</h2>
          <dl>
            <dt>Repository</dt>
            <dd>
              {repo?.html_url ? (
                <a href={repo.html_url} className="ul">
                  {repo.full_name}
                </a>
              ) : (
                (repo?.full_name ?? quickstart.repo_id)
              )}
            </dd>
            <dt>Doc followed</dt>
            <dd>{quickstart.doc_path}</dd>
            <dt>Goal</dt>
            <dd>{quickstart.goal}</dd>
            <dt>Branch</dt>
            <dd>{quickstart.branch || repo?.default_branch || "default"}</dd>
            {last && (
              <>
                <dt>Last run</dt>
                <dd>{when(last.started_at ?? last.queued_at)}</dd>
              </>
            )}
          </dl>
        </aside>
      </div>
    </>
  );
}
