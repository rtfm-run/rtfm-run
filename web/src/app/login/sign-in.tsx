"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ApiError, createApi, errorMessage } from "@/lib/api";
import Wordmark from "../wordmark";

type Phase = "checking" | "signed-out" | "unreachable";

export default function SignIn({ apiUrl }: { apiUrl: string }) {
  const router = useRouter();
  const api = useMemo(() => createApi(apiUrl), [apiUrl]);
  const [phase, setPhase] = useState<Phase>("checking");
  const [error, setError] = useState<string>();
  // The API sends failed sign-ins back here with a reason.
  const signInError = useSearchParams().get("error");

  useEffect(() => {
    if (api.sample) return;
    let live = true;
    api.me().then(
      () => {
        if (live) router.replace("/dashboard");
      },
      (e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 401) {
          setPhase("signed-out");
          return;
        }
        setError(errorMessage(e));
        setPhase("unreachable");
      },
    );
    return () => {
      live = false;
    };
  }, [api, router]);

  return (
    <div className="gate">
      <Link href="/">
        <Wordmark />
      </Link>

      {api.sample ? (
        <>
          <h1>Sign-in is not connected here.</h1>
          <p>This copy of the site has no API configured, so there is nothing to sign in to. You can still look around.</p>
          <Link className="btn" href="/dashboard">
            Open the sample dashboard
          </Link>
        </>
      ) : phase === "unreachable" ? (
        <>
          <h1>Sign-in is unavailable right now.</h1>
          <p className="err">{error}</p>
          <a className="btn" href="/login">
            Try again
          </a>
        </>
      ) : (
        <>
          <h1>Sign in to rtfm.run</h1>
          {signInError && <p className="err">{signInError}</p>}
          <p>
            Sign in with GitHub, then choose which repositories rtfm.run may read. It needs them to follow your docs
            and to open fix pull requests.
          </p>
          <a className="btn" href={api.loginUrl} aria-disabled={phase === "checking"}>
            Sign in with GitHub
          </a>
          <p className="faint">New here? Signing in creates your account.</p>
        </>
      )}
    </div>
  );
}
