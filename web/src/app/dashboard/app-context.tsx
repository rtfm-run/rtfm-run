"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { ApiError, createApi, errorMessage, type Api } from "@/lib/api";
import type { Quickstart, Repo, Run, User } from "@/lib/api-types";

type Data = {
  user?: User;
  installUrl: string;
  repos: Repo[];
  quickstarts: Quickstart[];
  runs: Run[];
};

type Phase = "loading" | "signed-out" | "ready" | "error";

type App = Data & {
  api: Api;
  phase: Phase;
  error?: string;
  refresh: () => Promise<void>;
  repoOf: (repoId: number) => Repo | undefined;
  lastRunOf: (quickstartId: number) => Run | undefined;
};

const EMPTY: Data = { installUrl: "", repos: [], quickstarts: [], runs: [] };

const AppContext = createContext<App | null>(null);

export function useApp(): App {
  const app = useContext(AppContext);
  if (!app) throw new Error("useApp must be used inside AppProvider");
  return app;
}

async function loadAll(api: Api): Promise<Data> {
  const me = await api.me();
  const repos = await api.repos();
  const [lists, runs] = await Promise.all([Promise.all(repos.map((r) => api.repoQuickstarts(r.id))), api.runs()]);
  return { user: me.user, installUrl: me.install_url, repos, quickstarts: lists.flat(), runs };
}

export function AppProvider({ apiUrl, children }: { apiUrl: string; children: React.ReactNode }) {
  const api = useMemo(() => createApi(apiUrl), [apiUrl]);
  const [data, setData] = useState<Data>(EMPTY);
  const [phase, setPhase] = useState<Phase>("loading");
  const [error, setError] = useState<string>();

  const refresh = useCallback(async () => {
    try {
      setData(await loadAll(api));
      setPhase("ready");
      setError(undefined);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setPhase("signed-out");
        return;
      }
      setError(errorMessage(e));
      setPhase((current) => (current === "ready" ? "ready" : "error"));
    }
  }, [api]);

  useEffect(() => {
    const first = setTimeout(refresh, 0);
    const timer = setInterval(refresh, 8000);
    return () => {
      clearTimeout(first);
      clearInterval(timer);
    };
  }, [refresh]);

  const app = useMemo<App>(() => {
    const repoOf = (repoId: number) => data.repos.find((r) => r.id === repoId);
    const lastRunOf = (quickstartId: number) => data.runs.find((r) => r.quickstart_id === quickstartId);
    return { ...data, api, phase, error, refresh, repoOf, lastRunOf };
  }, [data, api, phase, error, refresh]);

  return <AppContext.Provider value={app}>{children}</AppContext.Provider>;
}

export function useLoad<T>(load: () => Promise<T>) {
  const [state, setState] = useState<{ data?: T; error?: string; loading: boolean }>({ loading: true });
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let live = true;
    load().then(
      (data) => {
        if (live) setState({ data, loading: false });
      },
      (e) => {
        if (live) setState((prev) => ({ data: prev.data, error: errorMessage(e), loading: false }));
      },
    );
    return () => {
      live = false;
    };
  }, [load, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { ...state, reload };
}

export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const run = useCallback(async (action: () => Promise<void>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }, []);

  return { busy, error, run };
}
