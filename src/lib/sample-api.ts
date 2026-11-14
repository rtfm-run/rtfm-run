import { ApiError, type Api } from "./api";
import type { Quickstart, Repo, Run, RunDetail, RunStep, StepHealth, User } from "./api-types";

const USER: User = {
  id: 1,
  github_id: 1,
  login: "sample-user",
  name: "Sample User",
  avatar_url: "",
  created_at: "2026-11-14T08:00:00Z",
};

const REPOS: Repo[] = [
  {
    id: 101,
    installation_id: 1,
    full_name: "examples/stale-quickstart",
    default_branch: "main",
    private: false,
    html_url: "",
    updated_at: "2026-11-14T08:00:00Z",
  },
  {
    id: 102,
    installation_id: 1,
    full_name: "examples/good-quickstart",
    default_branch: "main",
    private: false,
    html_url: "",
    updated_at: "2026-11-14T08:00:00Z",
  },
  {
    id: 103,
    installation_id: 1,
    full_name: "example-org/webhooks-go",
    default_branch: "main",
    private: false,
    html_url: "",
    updated_at: "2026-11-14T08:00:00Z",
  },
];

function quickstart(id: number, repoId: number, goal: string): Quickstart {
  return {
    id,
    repo_id: repoId,
    name: "",
    doc_path: "README.md",
    goal,
    success_check: "",
    branch: "",
    backend: "auto",
    model: "",
    fix: true,
    verify: true,
    created_at: "2026-11-14T08:00:00Z",
    updated_at: "2026-11-14T08:00:00Z",
  };
}

const QUICKSTARTS: Quickstart[] = [
  quickstart(1, 101, "say hello from the CLI"),
  quickstart(2, 102, "start the server and see it respond"),
  quickstart(3, 103, "receive a test webhook locally"),
];

function step(index: number, instruction: string, docCommand: string, rest: Partial<RunStep> = {}): RunStep {
  return {
    stage: "newhire",
    index,
    step_key: `README.md#${index}`,
    doc_ref: "README.md",
    instruction,
    doc_command: docCommand,
    command: docCommand,
    exit_code: 0,
    status: "pass",
    failure: "",
    diagnosis: "",
    note: "",
    output: "",
    observed_exit: 0,
    checks_out: true,
    ...rest,
  };
}

function run(id: number, quickstartId: number, repoId: number, rest: Partial<Run>): Run {
  return {
    id,
    quickstart_id: quickstartId,
    repo_id: repoId,
    trigger: "manual",
    ref: "main",
    commit_sha: "4f47b1c0a95e57b0ab622cd05156cde069842600",
    status: "succeeded",
    verdict: "works",
    issues: 0,
    doc_issues: 0,
    fix_status: "",
    tokens: 0,
    attempt: 1,
    queued_at: "2026-11-14T08:40:00Z",
    started_at: "2026-11-14T08:40:05Z",
    finished_at: "2026-11-14T08:42:00Z",
    ...rest,
  };
}

const DETAILS: RunDetail[] = [
  {
    ...run(1, 1, 101, {
      verdict: "broken",
      issues: 2,
      doc_issues: 2,
      fix_status: "unverified",
      tokens: 55000,
      started_at: "2026-11-14T08:41:00Z",
      finished_at: "2026-11-14T08:42:54Z",
    }),
    steps: [
      step(1, "Clone the repo", "git clone https://github.com/acme/hello && cd hello", {
        output: "Cloning into 'hello'...",
      }),
      step(2, "Install it", "pip install -e .", { output: "Successfully installed hello-0.1.0" }),
      step(3, "Say hi", "hello --user newhire", {
        command: "hello --name newhire",
        status: "unsupported",
        failure: "stale_command",
        diagnosis: "Docs say `hello --user newhire`; what actually worked was `hello --name newhire`.",
        output: "Hello, newhire!",
        checks_out: false,
      }),
      step(4, "Ask the model", 'gemini "say hello"', {
        status: "fail",
        exit_code: 127,
        observed_exit: 127,
        failure: "missing_prereq",
        diagnosis: "`gemini` isn't installed, and no earlier step installs it.",
        output: "bash: gemini: command not found",
        checks_out: false,
      }),
    ],
    report: {
      id: "sample-1",
      target: "examples/stale-quickstart",
      repo: "https://github.com/examples/stale-quickstart",
      doc_path: "README.md",
      goal: "say hello from the CLI",
      runner: "agy",
      model: "gemini-3.8-flash-medium",
      sandbox: "devbox",
      started: "2026-11-14T08:41:00Z",
      ended: "2026-11-14T08:42:54Z",
      succeeded: false,
      gave_up_reason: "gemini is not installed and the doc never says how to get it",
      searches: ["install gemini cli"],
      commands: [
        { line: "git clone https://github.com/acme/hello && cd hello", exit_code: 0, output: "Cloning into 'hello'..." },
        { line: "pip install -e .", exit_code: 0, output: "Successfully installed hello-0.1.0" },
        {
          line: "hello --user newhire",
          exit_code: 2,
          output: "usage: hello [-h] --name NAME\nhello: error: unrecognized arguments: --user",
        },
        { line: "hello --help", exit_code: 0, output: "usage: hello [-h] --name NAME" },
        { line: "hello --name newhire", exit_code: 0, output: "Hello, newhire!" },
        { line: 'gemini "say hello"', exit_code: 127, output: "bash: gemini: command not found" },
      ],
      fix: {
        doc_path: "README.md",
        status: "unverified",
        issues_before: 2,
        issues_after: 0,
        patch: "--- a/README.md\n+++ b/README.md\n@@ -9,3 +9,3 @@\n 3. Say hi\n \n-       hello --user newhire\n+       hello --name newhire\n",
        changes: [
          {
            step_index: 3,
            summary: "Replace --user with --name.",
            evidence: "cli.py:7 defines --name; hello --help agrees",
            source: "run",
          },
        ],
        unresolved: [{ step_index: 4, reason: "The doc never says which tool provides `gemini`." }],
      },
    },
  },
  {
    ...run(2, 2, 102, {
      tokens: 31000,
      queued_at: "2026-11-14T08:44:00Z",
      started_at: "2026-11-14T08:44:02Z",
      finished_at: "2026-11-14T08:45:13Z",
    }),
    steps: [
      step(1, "Install dependencies", "npm install", { output: "added 1 package in 2s" }),
      step(2, "Start the server", "node server.js &", { output: "listening on :3000" }),
      step(3, "Check that it responds", "curl localhost:3000", { output: "ok" }),
    ],
    report: {
      id: "sample-2",
      target: "examples/good-quickstart",
      repo: "https://github.com/examples/good-quickstart",
      doc_path: "README.md",
      goal: "start the server and see it respond",
      runner: "agy",
      model: "gemini-3.8-flash-medium",
      sandbox: "devbox",
      started: "2026-11-14T08:44:02Z",
      ended: "2026-11-14T08:45:13Z",
      succeeded: true,
    },
  },
  {
    ...run(3, 3, 103, {
      issues: 1,
      doc_issues: 1,
      fix_status: "verified",
      pr_url: "",
      tokens: 59000,
      queued_at: "2026-11-14T09:01:00Z",
      started_at: "2026-11-14T09:01:03Z",
      finished_at: "2026-11-14T09:03:21Z",
    }),
    steps: [
      step(1, "Get the module", "go get github.com/example-org/webhooks-go"),
      step(2, "Start the listener", "go run ./cmd/listen", {
        command: "go run ./cmd/listen --secret test",
        status: "unsupported",
        failure: "doc_gap",
        diagnosis:
          "The listener exits without a signing secret. The doc mentions “your secret” once and never says what it is or how to pass it.",
        output: "listening on :8080",
        checks_out: false,
      }),
      step(3, "Send a test event", "go run ./cmd/send", { output: "delivered: 200 OK" }),
    ],
    report: {
      id: "sample-3",
      target: "example-org/webhooks-go",
      repo: "https://github.com/example-org/webhooks-go",
      doc_path: "README.md",
      goal: "receive a test webhook locally",
      runner: "agy",
      model: "gemini-3.8-flash-medium",
      sandbox: "devbox",
      started: "2026-11-14T09:01:03Z",
      ended: "2026-11-14T09:03:21Z",
      succeeded: true,
      fix: {
        doc_path: "README.md",
        status: "verified",
        issues_before: 1,
        issues_after: 0,
        patch: "--- a/README.md\n+++ b/README.md\n@@ -21,3 +21,3 @@\n 2. Start the listener\n \n-       go run ./cmd/listen\n+       go run ./cmd/listen --secret test\n",
        changes: [
          {
            step_index: 2,
            summary: "Pass --secret when starting the listener.",
            evidence: "cmd/listen/main.go:31 requires --secret",
            source: "run",
          },
        ],
      },
    },
  },
];

function summary(detail: RunDetail): Run {
  const { steps, report, verify_report, ...rest } = detail;
  void steps;
  void report;
  void verify_report;
  return rest;
}

function notFound(): never {
  throw new ApiError(404, "not found");
}

function readOnly(): never {
  throw new ApiError(0, "This is sample data. Set RTFM_API_URL to connect the real API.");
}

export const sampleApi: Api = {
  sample: true,
  loginUrl: "",
  me: async () => ({ user: USER, install_url: "" }),
  logout: async () => {},
  sync: async () => REPOS,
  installations: async () => ({
    installations: [{ id: 1, account_id: 1, account_login: "examples", account_type: "Organization", suspended: false }],
    install_url: "",
  }),
  repos: async () => REPOS,
  repoQuickstarts: async (repoId) => QUICKSTARTS.filter((q) => q.repo_id === repoId),
  createQuickstart: async () => readOnly(),
  quickstart: async (id) => {
    const found = QUICKSTARTS.find((q) => q.id === id) ?? notFound();
    const last = DETAILS.find((d) => d.quickstart_id === id);
    return { quickstart: found, triggers: [], last_run: last ? summary(last) : null };
  },
  updateQuickstart: async () => readOnly(),
  deleteQuickstart: async () => readOnly(),
  stepHealth: async (id) => {
    const detail = DETAILS.find((d) => d.quickstart_id === id);
    if (!detail) return [];
    return detail.steps.map(
      (s): StepHealth => ({
        step_key: s.step_key,
        index: s.index,
        doc_ref: s.doc_ref,
        instruction: s.instruction,
        doc_command: s.doc_command,
        status: s.status,
        checks_out: s.checks_out,
        failure: s.failure,
        diagnosis: s.diagnosis,
        run_id: detail.id,
        commit_sha: detail.commit_sha,
        checked_at: detail.finished_at ?? detail.queued_at,
        last_verified_run_id: s.checks_out ? detail.id : null,
        last_verified_commit: s.checks_out ? detail.commit_sha : null,
        last_verified_at: s.checks_out ? (detail.finished_at ?? null) : null,
      }),
    );
  },
  createTrigger: async () => readOnly(),
  updateTrigger: async () => readOnly(),
  deleteTrigger: async () => readOnly(),
  credentials: async () => [],
  setCredential: async () => readOnly(),
  deleteCredential: async () => readOnly(),
  createRun: async () => readOnly(),
  runs: async (filter = {}) =>
    DETAILS.filter((d) => !filter.quickstart || d.quickstart_id === filter.quickstart)
      .map(summary)
      .sort((a, b) => b.id - a.id),
  run: async (id) => DETAILS.find((d) => d.id === id) ?? notFound(),
  cancelRun: async () => readOnly(),
  streamRun: (_id, _onEvent, onEnd) => {
    const timer = setTimeout(onEnd, 0);
    return { close: () => clearTimeout(timer) };
  },
};
