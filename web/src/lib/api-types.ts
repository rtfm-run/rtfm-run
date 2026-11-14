export type User = {
  id: number;
  github_id: number;
  login: string;
  name: string;
  email?: string;
  avatar_url: string;
  created_at: string;
};

export type Installation = {
  id: number;
  account_id: number;
  account_login: string;
  account_type: string;
  suspended: boolean;
};

export type Repo = {
  id: number;
  installation_id: number;
  full_name: string;
  default_branch: string;
  private: boolean;
  html_url: string;
  updated_at: string;
};

export type Quickstart = {
  id: number;
  repo_id: number;
  name: string;
  doc_path: string;
  goal: string;
  success_check: string;
  branch: string;
  backend: string;
  model: string;
  fix: boolean;
  verify: boolean;
  created_at: string;
  updated_at: string;
};

export type QuickstartInput = Partial<
  Pick<Quickstart, "name" | "doc_path" | "goal" | "success_check" | "branch" | "backend" | "model" | "fix" | "verify">
>;

export type Trigger = {
  id: number;
  quickstart_id: number;
  kind: string;
  branches: string[];
  paths: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type TriggerInput = { kind?: string; branches?: string[]; paths?: string[]; enabled?: boolean };

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type Verdict = "" | "works" | "broken" | "blocked" | "error";

export type Run = {
  id: number;
  quickstart_id: number;
  repo_id: number;
  trigger: string;
  triggered_by?: number;
  ref: string;
  commit_sha: string;
  status: RunStatus;
  verdict: Verdict;
  issues: number;
  doc_issues: number;
  fix_status: string;
  pr_url?: string;
  report_id?: string;
  error?: string;
  tokens: number;
  attempt: number;
  queued_at: string;
  started_at?: string;
  finished_at?: string;
};

export type StepStatus = "pass" | "fail" | "skipped" | "unsupported";

export type RunStep = {
  stage: "newhire" | "verify";
  index: number;
  step_key: string;
  doc_ref: string;
  instruction: string;
  doc_command: string;
  command: string;
  exit_code: number;
  status: StepStatus;
  failure: string;
  diagnosis: string;
  note: string;
  output: string;
  observed_exit: number | null;
  checks_out: boolean;
};

export type ReportCommand = { line: string; exit_code: number; output?: string };

export type FixChange = { step_index: number; summary: string; evidence?: string; source: string };

export type FixResult = {
  doc_path: string;
  patch?: string;
  changes?: FixChange[];
  unresolved?: { step_index: number; reason: string }[];
  status: string;
  issues_before: number;
  issues_after: number;
  blocked_after?: number;
  blocked_on?: string[];
  verify_run_id?: string;
  error?: string;
  pr_url?: string;
};

export type ReportCredential = {
  name: string;
  description?: string;
  url?: string;
  status: "provided" | "unavailable" | "invalid";
  source?: string;
};

export type Report = {
  id: string;
  target: string;
  repo: string;
  base_commit?: string;
  doc_path: string;
  goal: string;
  runner: string;
  model?: string;
  sandbox: string;
  started: string;
  ended: string;
  succeeded: boolean;
  gave_up_reason?: string;
  commands?: ReportCommand[];
  searches?: string[];
  urls_read?: string[];
  success_check?: ReportCommand;
  credentials?: ReportCredential[];
  fix?: FixResult;
  error?: string;
};

export type RunDetail = Run & { report?: Report | null; verify_report?: Report | null; steps: RunStep[] };

export type StepHealth = {
  step_key: string;
  index: number;
  doc_ref: string;
  instruction: string;
  doc_command: string;
  status: StepStatus;
  checks_out: boolean;
  failure: string;
  diagnosis: string;
  run_id: number;
  commit_sha: string;
  checked_at: string;
  last_verified_run_id: number | null;
  last_verified_commit: string | null;
  last_verified_at: string | null;
};

export type RunEvent = { id: number; run_id: number; at: string; kind: string; data: Record<string, unknown> };

export type PRBlockerCode = "ai_forbidden" | "issue" | "dco" | "identity" | "cla" | "attest";

export type PRBlocker = { code: PRBlockerCode; message: string; hard?: boolean; items?: string[] };

export type PRDraft = {
  status: "ready" | "opening" | "opened";
  error?: string;
  pr_url?: string;
  opened_at?: string;
  partial: boolean;
  repo: string;
  title: string;
  body: string;
  commit_message: string;
  branch: string;
  base_branch: string;
  patch: string;
  found: string[];
  applied: string[];
  warnings: string[];
  blockers: PRBlocker[];
  identity: { name: string; email: string };
};

export type PROptions = {
  signoff?: boolean;
  cla_signed?: boolean;
  attest?: boolean;
  issue_url?: string;
  allow_partial?: boolean;
};

export type PRSummary = {
  run_id: number;
  status: "ready" | "opening" | "opened";
  title: string;
  partial: boolean;
  pr_url?: string;
  opened_at?: string;
  created_at: string;
  commit_sha: string;
};
