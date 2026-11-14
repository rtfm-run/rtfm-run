"use client";

import { useParams } from "next/navigation";
import QuickstartView from "./quickstart-view";
import RunView from "./run-view";

export default function ParamView({ kind }: { kind: "quickstart" | "run" }) {
  const id = Number(useParams<{ id: string }>().id);
  if (!Number.isInteger(id) || id <= 0) return <p className="err">Not found.</p>;
  return kind === "run" ? <RunView key={id} id={id} /> : <QuickstartView key={id} id={id} />;
}
