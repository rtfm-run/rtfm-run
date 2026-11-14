import type {
  Installation,
  Quickstart,
  QuickstartInput,
  Repo,
  Run,
  RunDetail,
  RunEvent,
  StepHealth,
  Trigger,
  TriggerInput,
  User,
} from "./api-types";
import { sampleApi } from "./sample-api";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong.";
}

export type RunStream = { close: () => void };

export type Api = {
  sample: boolean;
  loginUrl: string;
  me(): Promise<{ user: User; install_url: string }>;
  logout(): Promise<void>;
  sync(): Promise<Repo[]>;
  installations(): Promise<{ installations: Installation[]; install_url: string }>;
  repos(): Promise<Repo[]>;
  repoQuickstarts(repoId: number): Promise<Quickstart[]>;
  createQuickstart(repoId: number, input: QuickstartInput): Promise<Quickstart>;
  quickstart(id: number): Promise<{ quickstart: Quickstart; triggers: Trigger[]; last_run: Run | null }>;
  updateQuickstart(id: number, input: QuickstartInput): Promise<Quickstart>;
  deleteQuickstart(id: number): Promise<void>;
  stepHealth(id: number): Promise<StepHealth[]>;
  createTrigger(quickstartId: number, input: TriggerInput): Promise<Trigger>;
  updateTrigger(id: number, input: TriggerInput): Promise<Trigger>;
  deleteTrigger(id: number): Promise<void>;
  credentials(quickstartId: number): Promise<string[]>;
  setCredential(quickstartId: number, name: string, value: string): Promise<void>;
  deleteCredential(quickstartId: number, name: string): Promise<void>;
  createRun(quickstartId: number): Promise<Run>;
  runs(filter?: { quickstart?: number; repo?: number; limit?: number }): Promise<Run[]>;
  run(id: number): Promise<RunDetail>;
  cancelRun(id: number): Promise<Run>;
  streamRun(id: number, onEvent: (e: RunEvent) => void, onEnd: () => void): RunStream;
};

const EVENT_KINDS = [
  "queued",
  "started",
  "stage_started",
  "stage_finished",
  "finished",
  "failed",
  "cancel_requested",
  "canceled",
];

function httpApi(base: string): Api {
  async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
    let res: Response;
    try {
      res = await fetch(base + path, {
        method,
        credentials: "include",
        headers: method === "GET" ? undefined : { "Content-Type": "application/json" },
        body: method === "GET" || method === "DELETE" ? undefined : JSON.stringify(body ?? {}),
      });
    } catch {
      throw new ApiError(0, `Could not reach the API at ${base}.`);
    }
    if (res.status === 204) return undefined as T;
    const data = await res.json().catch(() => null);
    if (!res.ok) {
      throw new ApiError(res.status, (data && typeof data.error === "string" && data.error) || `Request failed (${res.status}).`);
    }
    return data as T;
  }

  return {
    sample: false,
    loginUrl: `${base}/auth/github/login`,
    me: () => call("GET", "/api/me"),
    logout: async () => {
      await fetch(`${base}/auth/logout`, { method: "POST", credentials: "include", mode: "no-cors" });
    },
    sync: async () => (await call<{ repos: Repo[] }>("POST", "/api/sync")).repos,
    installations: () => call("GET", "/api/installations"),
    repos: async () => (await call<{ repos: Repo[] }>("GET", "/api/repos")).repos,
    repoQuickstarts: async (repoId) =>
      (await call<{ quickstarts: Quickstart[] }>("GET", `/api/repos/${repoId}/quickstarts`)).quickstarts,
    createQuickstart: (repoId, input) => call("POST", `/api/repos/${repoId}/quickstarts`, input),
    quickstart: (id) => call("GET", `/api/quickstarts/${id}`),
    updateQuickstart: (id, input) => call("PATCH", `/api/quickstarts/${id}`, input),
    deleteQuickstart: (id) => call("DELETE", `/api/quickstarts/${id}`),
    stepHealth: async (id) => (await call<{ steps: StepHealth[] }>("GET", `/api/quickstarts/${id}/steps`)).steps ?? [],
    createTrigger: (quickstartId, input) => call("POST", `/api/quickstarts/${quickstartId}/triggers`, input),
    updateTrigger: (id, input) => call("PATCH", `/api/triggers/${id}`, input),
    deleteTrigger: (id) => call("DELETE", `/api/triggers/${id}`),
    credentials: async (quickstartId) =>
      (await call<{ credentials: string[] | null }>("GET", `/api/quickstarts/${quickstartId}/credentials`)).credentials ?? [],
    setCredential: (quickstartId, name, value) =>
      call("PUT", `/api/quickstarts/${quickstartId}/credentials/${encodeURIComponent(name)}`, { value }),
    deleteCredential: (quickstartId, name) =>
      call("DELETE", `/api/quickstarts/${quickstartId}/credentials/${encodeURIComponent(name)}`),
    createRun: (quickstartId) => call("POST", `/api/quickstarts/${quickstartId}/runs`),
    runs: async (filter = {}) => {
      const query = new URLSearchParams();
      if (filter.quickstart) query.set("quickstart", String(filter.quickstart));
      if (filter.repo) query.set("repo", String(filter.repo));
      query.set("limit", String(filter.limit ?? 100));
      return (await call<{ runs: Run[] }>("GET", `/api/runs?${query}`)).runs;
    },
    run: (id) => call("GET", `/api/runs/${id}`),
    cancelRun: (id) => call("POST", `/api/runs/${id}/cancel`),
    streamRun: (id, onEvent, onEnd) => {
      const source = new EventSource(`${base}/api/runs/${id}/events`, { withCredentials: true });
      const handle = (message: MessageEvent) => {
        try {
          onEvent(JSON.parse(message.data) as RunEvent);
        } catch {
          return;
        }
      };
      for (const kind of EVENT_KINDS) source.addEventListener(kind, handle);
      source.addEventListener("end", () => {
        source.close();
        onEnd();
      });
      return { close: () => source.close() };
    },
  };
}

export function createApi(base: string): Api {
  const trimmed = base.replace(/\/+$/, "");
  return trimmed ? httpApi(trimmed) : sampleApi;
}
