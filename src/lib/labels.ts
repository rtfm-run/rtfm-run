import type { Quickstart, Repo, Run, StepStatus } from "./api-types";

export type Standing = "new" | "running" | "works" | "unclear" | "broken" | "blocked" | "error" | "canceled";

export const STANDING_LABEL: Record<Standing, string> = {
  new: "Not run",
  running: "Running",
  works: "Works",
  unclear: "Unclear",
  broken: "Broken",
  blocked: "Blocked",
  error: "Error",
  canceled: "Canceled",
};

export function standingOf(run: Run | null | undefined): Standing {
  if (!run) return "new";
  if (run.status === "queued" || run.status === "running") return "running";
  if (run.status === "canceled") return "canceled";
  if (run.status === "failed" || run.verdict === "error") return "error";
  if (run.verdict === "blocked") return "blocked";
  if (run.verdict === "broken") return "broken";
  return run.issues > 0 ? "unclear" : "works";
}

export function runSummary(run: Run | null | undefined): string {
  const issues = (n: number) => `${n} step${n === 1 ? "" : "s"}`;
  switch (standingOf(run)) {
    case "new":
      return "Never run";
    case "running":
      return run?.status === "queued" ? "Waiting for a worker" : "The agent is following the doc now";
    case "canceled":
      return "The run was canceled";
    case "error":
      return run?.error ? `The run itself failed: ${run.error}` : "The run itself failed";
    case "blocked":
      return "Could not finish without a credential";
    case "broken":
      return `A new developer cannot finish. ${issues(run?.issues ?? 0)} would trip them`;
    case "unclear":
      return `It works, but ${issues(run?.issues ?? 0)} needed a guess`;
    case "works":
      return "Every step worked as written";
  }
}

export const STEP_LABEL: Record<StepStatus, string> = {
  pass: "Worked",
  fail: "Failed",
  unsupported: "Worked only by guessing",
  skipped: "Not reached",
};

const FAILURE_LABEL: Record<string, string> = {
  stale_command: "The command in the docs is out of date",
  missing_prereq: "The docs never install something this step needs",
  deprecated_api: "The docs use an API or model that was retired",
  doc_gap: "The docs leave out something a newcomer needs",
  broken_link: "A link in the docs is dead",
  environment: "The docs skip required setup, such as an API key",
  unknown: "Something went wrong here",
};

export function failureLabel(kind: string): string {
  return FAILURE_LABEL[kind] ?? "";
}

export const FIX_LABEL: Record<string, string> = {
  verified: "Verified: a fresh newcomer succeeded with the fixed doc",
  improved: "Improved: fewer issues with the fixed doc",
  not_fixed: "Not fixed: the rerun was no better",
  no_change: "No doc change proposed",
  unverified: "Proposed, not verified",
  blocked: "Proposed; verification blocked on credentials",
  error: "The fix run failed",
};

export const EVENT_LABEL: Record<string, string> = {
  queued: "Queued",
  started: "A worker picked it up",
  finished: "Finished",
  failed: "The run failed",
  cancel_requested: "Cancel requested",
  canceled: "Canceled",
};

const STAGE_LABEL: Record<string, string> = {
  newhire: "following the doc as a new developer",
  fix: "writing a fix",
  verify: "rerunning the doc with the fix",
};

export function eventText(kind: string, data: Record<string, unknown>): string {
  const stage = typeof data.stage === "string" ? (STAGE_LABEL[data.stage] ?? data.stage) : "";
  if (kind === "stage_started") return `Started ${stage}`;
  if (kind === "stage_finished") return `Finished ${stage}`;
  if (kind === "failed" && typeof data.error === "string") return `The run failed: ${data.error}`;
  if (kind === "finished" && typeof data.verdict === "string") return `Finished. Verdict: ${data.verdict}`;
  return EVENT_LABEL[kind] ?? kind;
}

export function quickstartTitle(q: Quickstart, repo: Repo | undefined): string {
  const name = repo?.full_name ?? `repo ${q.repo_id}`;
  return q.doc_path === "README.md" ? name : `${name} · ${q.doc_path}`;
}

export function when(iso: string | undefined | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

export function took(run: Run): string {
  if (!run.started_at || !run.finished_at) return "";
  const seconds = Math.max(0, Math.round((Date.parse(run.finished_at) - Date.parse(run.started_at)) / 1000));
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return m ? `${m}m${String(s).padStart(2, "0")}s` : `${s}s`;
}

export function shortSha(sha: string): string {
  return sha.slice(0, 7);
}
