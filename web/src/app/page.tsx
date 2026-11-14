import Link from "next/link";
import Wordmark from "./wordmark";
import "./nav.css";
import "./home.css";

type Item = { title: string; body: string; note?: string };

const STEPS: Item[] = [
  {
    title: "Connect a repository",
    body: "Sign in with GitHub, install the app on the repos you care about, and point rtfm.run at a doc: a README, a quickstart, an install guide.",
  },
  {
    title: "An agent follows it on a fresh machine",
    body: "On every push, an AI agent reads your doc and does exactly what it says, inside a blank, isolated microVM. No tribal knowledge, no shortcuts.",
  },
  {
    title: "Broken steps get fixed and proven",
    body: "When a step fails, it finds the cause, rewrites the doc, and has a second fresh agent follow the new version. Only a fix that works gets proposed.",
    note: "← the whole point",
  },
  {
    title: "You get a pull request",
    body: "The fix arrives as a pull request written to your repo's contribution guidelines, with the evidence for every change.",
  },
];

const FEATURES: Item[] = [
  {
    title: "Step-by-step health",
    body: "See which steps of each doc work today, and the last commit where every step was verified.",
  },
  {
    title: "Live runs",
    body: "Watch the agent work: every command it runs and every update it posts, as it happens.",
  },
  {
    title: "Fixes that are tested",
    body: "Every proposed change is rerun from scratch before it reaches you. If the rerun fails, the fix is thrown away.",
    note: "no vibes-based fixes",
  },
  {
    title: "Respects your project",
    body: "Pull requests follow your templates and CONTRIBUTING rules, disclose AI involvement, and are never sent to projects that refuse AI contributions.",
  },
  {
    title: "Isolated by default",
    body: "Each run gets its own throwaway microVM. Your credentials stay encrypted and are only handed to the steps that ask for them.",
  },
  {
    title: "Works with your stack",
    body: "Python, Node.js, Go, Docker and whatever else your docs install. If a newcomer could follow it, so can the agent.",
  },
];

export default function Home() {
  return (
    <main className="memo home">
      <header className="rfc-head">
        <div>
          <Wordmark />
        </div>
        <nav className="r topnav">
          <Link href="/dashboard">dashboard</Link>
          <Link href="/login">sign in</Link>
        </nav>
      </header>

      <h1>
        Nobody Has <span className="wavy">Run</span> Your Quickstart Since You Wrote It
      </h1>
      <p className="hand title-note">we did. step 4 exits 127.</p>

      <section className="hero">
        <p className="lead">
          rtfm.run has an AI agent follow your documentation on a fresh machine, the way a new developer would. When a
          step breaks, it fixes the doc, proves the fix works, and opens a pull request.
        </p>
        <p className="cta">
          <Link href="/login" className="button">
            Get started with GitHub
          </Link>
          <Link href="/dashboard" className="quiet">
            Open the dashboard →
          </Link>
        </p>
      </section>

      <section>
        <h2>How it works</h2>
        <ol className="how">
          {STEPS.map((s, i) => (
            <li key={s.title}>
              <span className="n">{i + 1}</span>
              <div>
                <h3>{s.title}</h3>
                <p>
                  {s.body}
                  {s.note && <span className="hand note">{s.note}</span>}
                </p>
              </div>
            </li>
          ))}
        </ol>
      </section>

      <section>
        <h2>What you get</h2>
        <div className="features">
          {FEATURES.map((f) => (
            <div key={f.title} className="feature">
              <h3>{f.title}</h3>
              <p>
                {f.body}
                {f.note && <span className="hand note">{f.note}</span>}
              </p>
            </div>
          ))}
        </div>
      </section>

      <section className="closing">
        <h2>Find out what your next user will hit, before they do.</h2>
        <p className="cta">
          <Link href="/login" className="button">
            Get started with GitHub
          </Link>
        </p>
      </section>

      <p className="hand end">now go run yours.</p>

      <footer className="foot">
        <span>rtfm.run</span>
        <span className="faint">Built on Gemini and the Antigravity CLI</span>
      </footer>
    </main>
  );
}
