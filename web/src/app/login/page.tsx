import type { Metadata } from "next";
import { connection } from "next/server";
import { Suspense } from "react";
import SignIn from "./sign-in";
import "../dashboard/dashboard.css";

export const metadata: Metadata = {
  title: "Sign in — rtfm.run",
};

export const instant = false;

async function Configured() {
  await connection();
  return <SignIn apiUrl={process.env.RTFM_API_URL ?? ""} />;
}

export default function LoginPage() {
  return (
    <Suspense fallback={<div className="gate" />}>
      <Configured />
    </Suspense>
  );
}
