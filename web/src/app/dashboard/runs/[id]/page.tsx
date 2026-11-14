import { Suspense } from "react";
import ParamView from "../../param-view";

export const instant = false;

export default function Page() {
  return (
    <Suspense fallback={<p className="faint">Loading…</p>}>
      <ParamView kind="run" />
    </Suspense>
  );
}
