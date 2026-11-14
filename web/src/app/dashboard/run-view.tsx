"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import type { ReportCredential, RunEvent, RunStep } from "@/lib/api-types";
import {
  clock,
  eventText,
  failureLabel,
  FIX_LABEL,
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
import PullRequest from "./pull-request";

function StepItem({ step }: { step: RunStep }) {
  const changed = step.command && step.doc_command && step.command !== step.doc_command;
  const problem = step.status === "fail" || step.status === "unsupported";
  const exit = step.observed_exit ?? step.exit_code;

  return (
    <li className={`step v-${step.status}`}>
      <span className="num">{step.index}.</span>
      <div>
        <div className="step-head">
          <h3>{step.instruction || step.doc_command || step.command}</h3>
          <span className={`st v-${step.status}`}>{STEP_LABEL[step.status]}</span>
        </div>

        {step.doc_command && (
          <div className="cmd">
            <span className="label">The docs say</span>
            <code>{step.doc_command}</code>
          </div>
        )}

        {problem && (
          <div className="card">
            <p>
              {failureLabel(step.failure) && <b>{failureLabel(step.failure)}. </b>}
              {step.diagnosis || step.note}
            </p>
            {changed && (
              <div className="cmd">
                <span className="label">What actually worked</span>
                <code>{step.command}</code>
              </div>
            )}
          </div>
        )}

        {(step.command || step.output) && step.status !== "skipped" && (
          <details className="evidence">
            <summary>What the agent ran</summary>
            <div className="log">
              <div className="attempt">
                {step.command && <div>$ {step.command}</div>}
                {step.output && <div className="out">{step.output}</div>}
                <div className={`out${exit === 0 ? "" : " bad"}`}>exit {exit}</div>
              </div>
            </div>
          </details>
        )}
      </div>
    </li>
  );
}

type EventGroup = { kind: "event"; event: RunEvent } | { kind: "commands"; items: RunEvent[] };

// groupEvents folds each run of consecutive commands into one group, so the
// transcript reads as the agent's updates with the commands tucked in between.
function groupEvents(events: RunEvent[]): EventGroup[] {
  const out: EventGroup[] = [];
  for (const e of events) {
    const prev = out[out.length - 1];
    if (e.kind !== "command") out.push({ kind: "event", event: e });
    else if (prev?.kind === "commands") prev.items.push(e);
    else out.push({ kind: "commands", items: [e] });
  }
  return out;
}

function exitOf(e: RunEvent): number | null {
  return typeof e.data.exit_code === "number" ? e.data.exit_code : null;
}

// CommandGroup is a collapsed row of commands. Its preview shows the latest
// command and swaps in each new one as it arrives; open it to see them all.
function CommandGroup({ items, live }: { items: RunEvent[]; live: boolean }) {
  const latest = items[items.length - 1];
  const failed = items.filter((e) => (exitOf(e) ?? 0) !== 0).length;
  return (
    <div className="line k-system">
      <span className="who">{clock(latest.at)}</span>
      <details className="cmds">
        <summary>
          <span key={latest.id} className={`cmd-peek${live ? " live" : ""}`}>
            {eventText(latest.kind, latest.data)}
          </span>
          <span className="cmd-count">
            {items.length} command{items.length === 1 ? "" : "s"}
            {failed > 0 ? `, ${failed} failed` : ""}
          </span>
        </summary>
        <div className="cmd-list">
          {items.map((e) => {
            const exit = exitOf(e);
            return (
              <div key={e.id} className="cmd-item">
                <span className="who">{clock(e.at)}</span>
                <span className="what">
                  {eventText(e.kind, e.data)}
                  {exit !== null && exit !== 0 && <span className="exit bad">exit {exit}</span>}
                  {e.data.timed_out === true && <span className="exit bad">timed out</span>}
                </span>
              </div>
            );
          })}
        </div>
      </details>
    </div>
  );
}

export default function RunView({ id }: { id: number }) {
  const app = useApp();
  const cancel = useAction();
  const [events, setEvents] = useState<RunEvent[]>([]);

  const load = useCallback(() => app.api.run(id), [app.api, id]);
  const { data: run, loading, error, reload } = useLoad(load);
  const refresh = app.refresh;

  useEffect(() => {
    const stream = app.api.streamRun(
      id,
      (e) => {
        setEvents((prev) => (prev.some((p) => p.id === e.id) ? prev : [...prev, e]));
        reload();
      },
      () => {
        reload();
        refresh();
      },
    );
    return () => stream.close();
  }, [app.api, id, reload, refresh]);

  if (loading && !run) return <p className="faint">Loading…</p>;
  if (!run) return <p className="err">{error ?? "Run not found."}</p>;

  const quickstart = app.quickstarts.find((q) => q.id === run.quickstart_id);
  const repo = app.repoOf(run.repo_id);
  const standing = standingOf(run);
  const active = run.status === "queued" || run.status === "running";
  const walked = run.steps.filter((s) => s.stage === "newhire");
  const verified = run.steps.filter((s) => s.stage === "verify");
  const report = run.report ?? undefined;
  const fix = report?.fix;
  const prUrl = run.pr_url || fix?.pr_url;
  const groups = groupEvents(events);
  // Credentials the agent asked for but could not get (in either pass).
  const missing = new Map<string, ReportCredential>();
  for (const c of [...(report?.credentials ?? []), ...(run.verify_report?.credentials ?? [])]) {
    if (c.status !== "provided" && !missing.has(c.name)) missing.set(c.name, c);
  }

  return (
    <>
      <Link href={`/dashboard/quickstarts/${run.quickstart_id}`} className="back shown">
        ← {quickstart ? quickstartTitle(quickstart, repo) : "Quickstart"}
      </Link>

      <header className="report-head">
        <div className="title-row">
          <h1>Run #{run.id}</h1>
          <span className={`st big s-${standing}`}>{STANDING_LABEL[standing]}</span>
          {active && (
            <button
              type="button"
              className="act push"
              disabled={cancel.busy}
              onClick={() =>
                cancel.run(async () => {
                  await app.api.cancelRun(id);
                  reload();
                })
              }
            >
              [cancel]
            </button>
          )}
          {prUrl && (
            <a className="btn push" href={prUrl}>
              Open the pull request
            </a>
          )}
        </div>
        <p className="lead">
          {runSummary(run)}.{report?.gave_up_reason ? ` The agent gave up: ${report.gave_up_reason}.` : ""}
        </p>
        {cancel.error && <p className="err">{cancel.error}</p>}
      </header>

      <div className="report">
        <div>
          {!active && missing.size > 0 && (
            <section className="block">
              <h2>Needs credentials</h2>
              <p className="faint">
                The agent asked for these and could not get them, so the steps that need them could not be checked.
              </p>
              {[...missing.values()].map((c) => (
                <div key={c.name} className="card open">
                  <p>
                    <b>{c.name}</b>
                    {c.status === "invalid" ? " was rejected as invalid." : " was not available."}
                    {c.description ? ` ${c.description}` : ""}
                  </p>
                  {c.url && (
                    <p className="faint">
                      Get one at{" "}
                      <a href={c.url} className="ul" rel="noreferrer noopener" target="_blank">
                        {c.url}
                      </a>
                    </p>
                  )}
                </div>
              ))}
              <p>
                <Link href={`/dashboard/quickstarts/${run.quickstart_id}#credentials`} className="ul">
                  Add them to this quickstart
                </Link>
                , then run it again.
              </p>
            </section>
          )}

          {(active || events.length > 0) && (
            <section className="block">
              <h2>Progress</h2>
              <div className="transcript">
                {groups.map((g, i) =>
                  g.kind === "commands" ? (
                    <CommandGroup key={g.items[0].id} items={g.items} live={active && i === groups.length - 1} />
                  ) : (
                    <div key={g.event.id} className={`line${g.event.kind === "progress" ? " k-user" : ""}`}>
                      <span className="who">{clock(g.event.at)}</span>
                      <span className="what">{eventText(g.event.kind, g.event.data)}</span>
                    </div>
                  ),
                )}
                {active && (
                  <div className="line">
                    <span className="who" />
                    <span className="cursor" aria-label="the run is in progress" />
                  </div>
                )}
              </div>
            </section>
          )}

          {walked.length > 0 && (
            <section className="block">
              <h2>Steps</h2>
              <ol className="steps">
                {walked.map((s) => (
                  <StepItem key={s.index} step={s} />
                ))}
              </ol>
            </section>
          )}

          {fix && (
            <section className="block">
              <h2>Fix</h2>
              <p>
                <b>{FIX_LABEL[fix.status] ?? fix.status}.</b>
                {fix.verify_run_id ? ` Doc issues went from ${fix.issues_before} to ${fix.issues_after}.` : ""}
                {fix.error ? ` ${fix.error}` : ""}
              </p>
              {fix.changes?.map((c, i) => (
                <div key={i} className="card">
                  <p>
                    <b>Step {c.step_index}.</b> {c.summary}
                  </p>
                  {c.evidence && <p className="faint">Evidence: {c.evidence}</p>}
                </div>
              ))}
              {fix.unresolved?.map((u, i) => (
                <div key={i} className="card open">
                  <p>
                    <b>Step {u.step_index}, not fixed.</b> {u.reason}
                  </p>
                </div>
              ))}
              {fix.patch && (
                <pre className="patch">
                  {fix.patch.split("\n").map((line, i) => (
                    <span
                      key={i}
                      className={
                        line.startsWith("+") && !line.startsWith("+++")
                          ? "plus"
                          : line.startsWith("-") && !line.startsWith("---")
                            ? "minus"
                            : undefined
                      }
                    >
                      {line}
                      {"\n"}
                    </span>
                  ))}
                </pre>
              )}
            </section>
          )}

          {!active && <PullRequest key={`${run.status}:${run.pr_url ?? ""}`} runId={run.id} />}

          {verified.length > 0 && (
            <section className="block">
              <h2>Rerun with the fix</h2>
              <ol className="steps">
                {verified.map((s) => (
                  <StepItem key={s.index} step={s} />
                ))}
              </ol>
            </section>
          )}

          {report?.commands && report.commands.length > 0 && (
            <section className="block">
              <details className="evidence">
                <summary>Every command the agent ran ({report.commands.length})</summary>
                <div className="log">
                  {report.commands.map((c, i) => (
                    <div key={i} className="attempt">
                      <div>$ {c.line}</div>
                      {c.output && <div className="out">{c.output}</div>}
                      <div className={`out${c.exit_code === 0 ? "" : " bad"}`}>
                        {c.exit_code < 0 ? "exit unknown" : `exit ${c.exit_code}`}
                      </div>
                    </div>
                  ))}
                  {report.searches?.map((q) => (
                    <div key={q} className="attempt">
                      searched the web: “{q}”
                    </div>
                  ))}
                </div>
              </details>
            </section>
          )}

          {!active && walked.length === 0 && run.error && <p className="err">{run.error}</p>}
        </div>

        <aside className="rail">
          <h2>Details</h2>
          <dl>
            <dt>Quickstart</dt>
            <dd>
              <Link href={`/dashboard/quickstarts/${run.quickstart_id}`} className="ul">
                {quickstart ? quickstartTitle(quickstart, repo) : run.quickstart_id}
              </Link>
            </dd>
            {report?.goal && (
              <>
                <dt>Goal</dt>
                <dd>{report.goal}</dd>
              </>
            )}
            <dt>Started by</dt>
            <dd>{run.trigger === "push" ? "a push" : "Run now"}</dd>
            <dt>Commit</dt>
            <dd>{run.commit_sha ? shortSha(run.commit_sha) : run.ref || "branch head"}</dd>
            <dt>Started</dt>
            <dd>{when(run.started_at ?? run.queued_at)}</dd>
            {took(run) && (
              <>
                <dt>Took</dt>
                <dd>{took(run)}</dd>
              </>
            )}
            {report?.model && (
              <>
                <dt>Agent model</dt>
                <dd>{report.model}</dd>
              </>
            )}
            {run.tokens > 0 && (
              <>
                <dt>Tokens</dt>
                <dd>{run.tokens.toLocaleString()}</dd>
              </>
            )}
          </dl>
        </aside>
      </div>
    </>
  );
}
