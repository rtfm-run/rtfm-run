import { Suspense } from "react";
import NewQuickstart from "./new-quickstart";

export const instant = false;

export default function NewQuickstartPage() {
  return (
    <Suspense fallback={<p className="faint">Loading…</p>}>
      <NewQuickstart />
    </Suspense>
  );
}
