"use client";

import Link from "next/link";
import { useCallback } from "react";
import { useAction, useApp, useLoad } from "../app-context";

export default function Connections() {
  const app = useApp();
  const sync = useAction();
  const load = useCallback(() => app.api.installations(), [app.api]);
  const { data, reload } = useLoad(load);

  const installations = data?.installations ?? [];
  const installUrl = data?.install_url || app.installUrl;

  function syncNow() {
    sync.run(async () => {
      await app.api.sync();
      await app.refresh();
      reload();
    });
  }

  return (
    <>
      <Link href="/dashboard" className="back">
        ← Dashboard
      </Link>
      <header className="report-head">
        <h1>Connections</h1>
        <p className="lead">Where vanguard.run reads your docs from and opens its pull requests.</p>
      </header>

      <div className="sheet">
        <section className="conn">
          <div className="conn-row">
            <h2>GitHub</h2>
            <span className="dots" aria-hidden="true" />
            <span className={`st ${installations.length ? "s-running" : "s-new"}`}>
              {installations.length ? "Connected" : "Not installed"}
            </span>
          </div>
          <p className="faint">
            Signed in as {app.user?.login}.{" "}
            {installations.length
              ? `The GitHub App is installed on ${installations.map((i) => i.account_login).join(", ")}.`
              : "Install the GitHub App on the repositories you want checked."}
          </p>
          <div className="actions">
            {installUrl && (
              <a className="btn" href={installUrl}>
                {installations.length ? "Add repositories on GitHub" : "Install the GitHub App"}
              </a>
            )}
            <button type="button" className="act" disabled={sync.busy} onClick={syncNow}>
              [{sync.busy ? "syncing…" : "sync with GitHub"}]
            </button>
          </div>
          {sync.error && <p className="err">{sync.error}</p>}

          <ul className="repo-list">
            {app.repos.map((r) => (
              <li key={r.id}>
                <span>
                  {r.full_name}
                  {r.private ? " (private)" : ""}
                </span>
                <Link href={`/dashboard/new?repo=${r.id}`}>add a quickstart</Link>
              </li>
            ))}
          </ul>
        </section>

        <section className="conn">
          <div className="conn-row">
            <h2>GitLab</h2>
            <span className="dots" aria-hidden="true" />
            <span className="st s-new">Not available yet</span>
          </div>
          <p className="faint">The API only supports GitHub today.</p>
        </section>
      </div>
    </>
  );
}
