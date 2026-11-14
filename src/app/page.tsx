import Link from "next/link";
import CopyButton from "./copy-button";
import RunReplay from "./run-replay";
import Wordmark from "./wordmark";

const GITHUB = "https://github.com/rtfm-run";
const BADGE_MD =
  "[![quickstart](https://rtfm.run/badge/your-org/your-repo.svg)](https://rtfm.run/r/your-org/your-repo)";

const LOOP = `   your docs
       |
       v
   RUN        a new hire follows them, literally
   RECORD     command, exit code, output
   DIAGNOSE   stale command? missing prereq?
   FIX        the corrected line, with evidence
   VERIFY     fresh sandbox, full rerun
       |         fails? the fix is thrown away
       v
   SHIP       pull request, report, badge`;

function PageBreak({ page }: { page: number }) {
  return (
    <div className="pagebreak" aria-hidden="true">
      <div>
        <span>rtfm.run</span>
        <span className="mid">Standards Track</span>
        <span>[Page {page}]</span>
      </div>
      <hr />
      <div>
        <span>RFC 127</span>
        <span className="mid">Nobody Has Run Your Quickstart</span>
        <span>November 2026</span>
      </div>
    </div>
  );
}

export default function Home() {
  return (
    <main className="memo">
      <header className="rfc-head">
        <div>
          <Wordmark />
        </div>
        <div className="r">
          <Link href="/dashboard">dashboard</Link> · <Link href="/login">sign in</Link> ·{" "}
          <a className="hl" href={GITHUB}>
            github
          </a>
        </div>
      </header>

      <h1>
        Nobody Has <span className="wavy">Run</span> Your Quickstart Since You Wrote It
      </h1>
      <p className="hand title-note">we did. step 4 exits 127.</p>

      <h2>Abstract</h2>
      <p className="t">
        This document specifies rtfm.run, an agent that follows a quickstart the way a new developer would:
        literally, on a blank machine, with no idea what the author meant. It finds every step that breaks,
        writes the fix, and proves the fix works before it opens the pull request.
      </p>

      <h2>Status of This Memo</h2>
      <p className="t">
        This memo describes a system that executes documentation. It is not a linter and it does not have
        opinions about your Oxford commas. Distribution of this memo is unlimited, and encouraged.
      </p>

      <h2 id="intro">1. Introduction</h2>
      <p className="t">
        A quickstart is written once, by someone who already has everything installed. It is then read by
        people who have nothing installed. In between, the CLI gets renamed, a flag changes, a model ID is
        retired, and nobody finds out, because the only people who hit the error are new, and new people leave
        quietly.
        <span className="hand note">every one of them was a signup</span>
      </p>
      <p className="t">
        Drift tools read your diffs and report that the docs <i>might</i> be wrong. rtfm.run runs them.
        Your quickstart MUST work (RFC 2119). The remainder of this document concerns what happens when it
        does not.
      </p>

      <h2 id="protocol">2. Protocol Overview</h2>
      <p className="t">
        The input is a repository, a docs URL or a notebook, plus a goal such as “make your first API call”.
        No test scripts are written and no markup is added to the docs.
      </p>
      <figure className="fig">
        <pre>{LOOP}</pre>
        <figcaption>Figure 1: The loop.</figcaption>
      </figure>
      <p className="t">
        A fix that has not survived VERIFY MUST NOT reach SHIP.
        <span className="hand note">← the whole point</span>
      </p>

      <PageBreak page={1} />

      <h2 id="run">3. Example Run</h2>
      <p className="t">
        A run against a four-step quickstart with two defects planted in it. The agent may search the web when
        stuck, because that is what a person would do.
      </p>
      <RunReplay />
      <p className="t">
        Steps 3 and 4 are from our first end-to-end run; steps 1 and 2 are illustrative. Both planted defects
        were found, and all 14 commands ran inside the container.
      </p>

      <h2 id="verdicts">4. Verdicts</h2>
      <dl className="defs">
        <dt>pass</dt>
        <dd>The docs said what to do, and it worked.</dd>
        <dt>fail</dt>
        <dd>The docs said what to do, and it did not work.</dd>
        <dt>skipped</dt>
        <dd>An earlier step never finished.</dd>
        <dt>
          <span className="hl">unsupported</span>
        </dt>
        <dd>
          <span className="hl">It worked, but only because the agent guessed. The docs never said how.</span>
          <span className="hand note">nobody else flags this one</span>
        </dd>
      </dl>
      <p className="t">
        Agents fill gaps from memory, silently, which makes broken docs look fine. A human on their first day
        has no such memory. Implementations MUST report every guess.
      </p>

      <h2 id="fixes">5. Fixes</h2>
      <p className="t">
        A fix is a minimal edit to the doc, justified by evidence from the repository itself and confirmed by
        a complete rerun in a fresh sandbox.
      </p>
      <figure className="fig">
        <pre>
          {"   3. Say hi\n\n          hello "}
          <del>--user</del>
          <span className="hand" style={{ margin: "0 1ch" }}>
            --name
          </span>
          {"newhire\n\n   4. Ask the model\n\n          "}
          {'gemini "say hello"'}
          <span className="hand" style={{ marginLeft: "2ch" }}>
            never installed!
          </span>
        </pre>
        <figcaption>Figure 3: README.md, corrected.</figcaption>
      </figure>
      <p className="t">
        Evidence: <code>cli.py:7</code> defines <code>--name</code>, and <code>hello --help</code> agrees.
      </p>

      <PageBreak page={2} />

      <h2 id="security">6. Security Considerations</h2>
      <p className="t">
        Running a stranger’s README is running a stranger’s code. Every run gets a container that is created
        for it and destroyed after it: a blank Ubuntu machine with no AI CLIs, no cloud SDKs and no package
        caches. The agent’s own shell is denied; its only way to run a command is a single tool that executes
        inside the container. Credentials stay on the host. Every command, exit code and output is logged, and
        that log is what gets graded, not the agent’s account of itself.
      </p>

      <h2 id="iana">7. IANA Considerations</h2>
      <p className="t">This document has no IANA actions. It does register one badge:</p>
      <figure className="fig">
        <pre>{"   [ quickstart | verified · 3h ago ]\n   [ quickstart | broken at step 4  ]"}</pre>
      </figure>
      <p className="t">If your quickstart breaks on Tuesday, the badge says so on Tuesday.</p>
      <p className="snippet">
        {BADGE_MD} <CopyButton text={BADGE_MD} />
      </p>

      <h2 id="address">Authors’ Address</h2>
      <p className="t">
        rtfm.run
        <br />
        Built on Gemini and the Antigravity CLI
        <br />
        <a className="hl" href={GITHUB}>
          github.com/rtfm-run
        </a>
      </p>

      <p className="hand end">now go run yours.</p>
    </main>
  );
}
