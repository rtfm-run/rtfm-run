import type { Metadata } from "next";
import { connection } from "next/server";
import { Suspense } from "react";
import { AppProvider } from "./app-context";
import Shell from "./shell";
import "./dashboard.css";

export const metadata: Metadata = {
  title: "Dashboard — vanguard.run",
  description: "Every quickstart vanguard.run has walked, step by step.",
};

async function Configured({ children }: { children: React.ReactNode }) {
  await connection();
  const apiUrl = process.env.VANGUARD_API_URL ?? "";
  return (
    <AppProvider apiUrl={apiUrl}>
      <Shell>{children}</Shell>
    </AppProvider>
  );
}

export default function DashboardLayout({ children }: LayoutProps<"/dashboard">) {
  return (
    <Suspense
      fallback={
        <div className="dash">
          <p className="pad faint">Loading…</p>
        </div>
      }
    >
      <Configured>{children}</Configured>
    </Suspense>
  );
}
