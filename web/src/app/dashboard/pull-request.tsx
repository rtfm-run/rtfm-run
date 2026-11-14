"use client";

import { useCallback, useState } from "react";
import type { PRBlocker, PRDraft } from "@/lib/api-types";
import { when } from "@/lib/labels";
import { useAction, useApp, useLoad } from "./app-context";

function Toggle({ on, onChange, children }: { on: boolean; onChange: (v: boolean) => void; children: React.ReactNode }) {
  return (
    <button type="button" className={`check${on ? " on" : ""}`} aria-pressed={on} onClick={() => onChange(!on)}>
      {children}
    </button>
  );
}

// Answers only the person opening the PR can give. Nothing is pre-ticked.
type Answers = { signoff: boolean; cla_signed: boolean; attest: boolean; issue_url: string; allow_partial: boolean };

const NO_ANSWERS: Answers = { signoff: false, cla_signed: false, attest: false, issue_url: "", allow_partial: false };

function answered(b: PRBlocker, a: Answers): boolean {
  switch (b.code) {
    case "dco":
      return a.signoff;
    case "cla":
      return a.cla_signed;
    case "attest":
      return a.attest;
    case "issue":
      return /^https:\/\/\S+$/.test(a.issue_url.trim());
    default:
      return false; // ai_forbidden can't be answered; identity is always supplied
  }
}

function Requirement({ b, draft, a, set }: { b: PRBlocker; draft: PRDraft; a: Answers; set: (a: Answers) => void }) {
  switch (b.code) {
    case "dco":
      return (
        <div className="card">
          <p>
            <b>This project requires a DCO sign-off.</b> Signing off certifies that you have the right to submit this change
            under the project&apos;s license (
            <a className="ul" href="https://developercertificate.org/" target="_blank" rel="noreferrer noopener">
              developercertificate.org
            </a>
            ). The commit will be authored and signed off by you.
          </p>
          <Toggle on={a.signoff} onChange={(v) => set({ ...a, signoff: v })}>
            Sign off as {draft.identity.name} &lt;{draft.identity.email}&gt;
          </Toggle>
        </div>
      );
    case "cla":
      return (
        <div className="card">
          <p>
            <b>This project requires a signed Contributor License Agreement.</b> Sign it yourself first; rtfm.run can&apos;t
            sign it for you.
          </p>
          <Toggle on={a.cla_signed} onChange={(v) => set({ ...a, cla_signed: v })}>
            I have signed this project&apos;s CLA
          </Toggle>
        </div>
      );
    case "attest":
      return (
        <div className="card">
          <p>
            <b>The pull request template has items only you can confirm:</b>
          </p>
          <ul>
            {b.items?.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
          <Toggle on={a.attest} onChange={(v) => set({ ...a, attest: v })}>
            These are true; tick them in the pull request
          </Toggle>
        </div>
      );
    case "issue":
      return (
        <div className="card">
          <p>
            <b>This project wants an issue before a pull request.</b> Open or find one, then link it here.
          </p>
          <input
            className="wide"
            value={a.issue_url}
            onChange={(e) => set({ ...a, issue_url: e.target.value })}
            placeholder="https://github.com/owner/repo/issues/123"
            aria-label="Issue URL"
          />
        </div>
      );
    case "ai_forbidden":
      return (
        <div className="card open">
          <p className="err">
            <b>This project does not accept AI-generated contributions,</b> so rtfm.run won&apos;t open a pull request. The
            fix is still shown below if you want to make the change yourself.
          </p>
        </div>
      );
    default:
      return (
        <div className="card">
          <p>{b.message}</p>
        </div>
      );
  }
}

// The parent remounts this (via key) when the run moves on, so a draft made
// at the end of the run shows up without a reload.
export default function PullRequest({ runId }: { runId: number }) {
  const app = useApp();
  const open = useAction();
  const [answers, setAnswers] = useState<Answers>(NO_ANSWERS);
  const load = useCallback(() => app.api.prDraft(runId), [app.api, runId]);
  const { data: draft, reload } = useLoad(load);

  if (!draft) return null;

  if (draft.status === "opened" && draft.pr_url) {
    return (
      <section className="block" id="pull-request">
        <h2>Pull request</h2>
        <p>
          Opened{draft.opened_at ? ` ${when(draft.opened_at)}` : ""}:{" "}
          <a className="ul" href={draft.pr_url} target="_blank" rel="noreferrer noopener">
            {draft.pr_url.replace("https://github.com/", "")}
          </a>
        </p>
      </section>
    );
  }

  const hard = draft.blockers.some((b) => b.hard);
  const missing = draft.blockers.filter((b) => !answered(b, answers));
  const ready = !hard && missing.length === 0 && (!draft.partial || answers.allow_partial);
  const busy = open.busy || draft.status === "opening";

  function submit() {
    open.run(async () => {
      await app.api.openPR(runId, {
        signoff: answers.signoff,
        cla_signed: answers.cla_signed,
        attest: answers.attest,
        issue_url: answers.issue_url.trim(),
        allow_partial: answers.allow_partial,
      });
      reload();
      await app.refresh();
    });
  }

  return (
    <section className="block" id="pull-request">
      <h2>Pull request</h2>
      <p>
        Ready to propose to <b>{draft.repo}</b>: <code>{draft.base_branch || "default branch"}</code> ←{" "}
        <code>{draft.branch}</code>. It will be opened by the rtfm.run GitHub App and says that an AI agent found and verified
        the fix.
      </p>

      <div className="card">
        <p>
          <b>{draft.title}</b>
        </p>
        <details className="evidence">
          <summary>Pull request description</summary>
          <pre className="pr-text">{draft.body}</pre>
        </details>
        <details className="evidence">
          <summary>Commit message</summary>
          <pre className="pr-text">{draft.commit_message}</pre>
        </details>
      </div>

      {draft.found.length > 0 && (
        <p className="faint">
          Follows the repository&apos;s guidelines: {draft.found.join(", ")}.
          {draft.applied.length > 0 ? ` ${draft.applied.join(". ")}.` : ""}
        </p>
      )}
      {draft.warnings.map((w) => (
        <p key={w} className="faint">
          Note: {w}
        </p>
      ))}

      {draft.blockers.map((b) => (
        <Requirement key={b.code} b={b} draft={draft} a={answers} set={setAnswers} />
      ))}

      {draft.partial && (
        <div className="card open">
          <p>
            <b>This fix is partial:</b> the rerun had fewer problems, but not none.
          </p>
          <Toggle on={answers.allow_partial} onChange={(v) => setAnswers({ ...answers, allow_partial: v })}>
            Propose it anyway
          </Toggle>
        </div>
      )}

      {!hard && (
        <p>
          <button type="button" className="btn" disabled={!ready || busy} onClick={submit}>
            {busy ? "Opening…" : "Open pull request"}
          </button>
        </p>
      )}
      {(open.error || draft.error) && <p className="err">{open.error ?? draft.error}</p>}
    </section>
  );
}
