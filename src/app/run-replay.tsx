"use client";

import { useEffect, useState } from "react";

type Line = {
  text: string;
  gap?: boolean;
  circle?: string;
  pen?: string;
  wait?: number;
};

const LINES: Line[] = [
  { text: "$ rtfm run acme/hello --doc README.md", wait: 900 },
  { text: "  devbox  fresh container, ubuntu 24.04, no caches" },
  { text: "  agent   newhire, gemini-3.8-flash, knows nothing", wait: 800 },

  { text: "  step 1  git clone https://github.com/acme/hello", gap: true },
  { text: "          exit 0", pen: "✓", wait: 600 },

  { text: "  step 2  pip install -e .", gap: true },
  { text: "          exit 0", pen: "✓", wait: 600 },

  { text: "  step 3  hello --user newhire", gap: true },
  { text: "          error: unrecognized arguments: --user" },
  { text: "          exit 2", wait: 700 },
  { text: "        > hello --help" },
  { text: "          usage: hello [-h] --name NAME", wait: 700 },
  { text: "        > hello --name newhire" },
  {
    text: "          Hello, newhire!",
    pen: "it guessed. the doc never says --name",
    wait: 1100,
  },

  { text: '  step 4  gemini "say hello"', gap: true },
  { text: "          bash: gemini: command not found" },
  { text: "          exit 127", circle: "exit 127", pen: "this is where they quit", wait: 900 },
  { text: '        > search_web "install gemini cli"', wait: 1000 },

  { text: "  2 pass, 1 unsupported, 1 fail. 1m54s, 55K tokens.", gap: true },
];

const TOTAL = LINES.length;

export default function RunReplay() {
  const [n, setN] = useState(0);

  useEffect(() => {
    if (n >= TOTAL) return;
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const delay = reduced ? 0 : n === 0 ? 500 : (LINES[n - 1].wait ?? 320);
    const id = setTimeout(() => setN(reduced ? TOTAL : n + 1), delay);
    return () => clearTimeout(id);
  }, [n]);

  return (
    <figure className="fig">
      <div className="run">
        {LINES.map((l, i) => {
          let body: React.ReactNode = l.text;
          if (l.circle) {
            const at = l.text.indexOf(l.circle);
            body = (
              <>
                {l.text.slice(0, at)}
                <span className="circ">{l.circle}</span>
                {l.text.slice(at + l.circle.length)}
              </>
            );
          }
          return (
            <div key={i} className={`l${l.gap ? " gap" : ""}${i < n ? " on" : ""}`}>
              {body}
              {l.pen && <span className="hand">{l.pen}</span>}
            </div>
          );
        })}
      </div>
      <figcaption>
        Figure 2: A run, annotated.{" "}
        <button type="button" className="linkbtn" onClick={() => setN(0)} disabled={n < TOTAL}>
          [replay]
        </button>
      </figcaption>
    </figure>
  );
}
