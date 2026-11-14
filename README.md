# front

Landing page for [rtfm.run](https://rtfm.run): an agent that follows your quickstart the way a new developer would, finds every step that breaks, writes the fix, and proves the fix works.

```bash
npm install
npm run dev   # http://localhost:3000
```

Next.js (App Router) + TypeScript. The page is `src/app/page.tsx`; the hero replay is `src/app/run-replay.tsx`.

## Connecting the API

The dashboard (`/dashboard`, `/login`) talks to the rtfm API (`rtfm serve` in the `rtfm.run` repo) straight from the browser, with the API's session cookie. Point it at the API with `RTFM_API_URL`, read at request time (no rebuild needed; `next dev` also reloads `.env.local` on change):

```bash
echo 'RTFM_API_URL=http://localhost:8080' > .env.local
npm run dev
```

On the API side, set `RTFM_DASHBOARD_URL` to this app's `/dashboard` URL. The API uses it to:

- allow this origin to make credentialed CORS requests (`/api/*`, including the run event stream)
- send people here after sign-in, and back to `/login?error=…` when sign-in fails
- redirect its own `/` to the dashboard

The session cookie is `SameSite=Lax`, so the dashboard and the API must be on the same site: two ports on `localhost` in development, or `rtfm.run` and `api.rtfm.run` in production.

Local development, end to end:

```bash
# in rtfm.run/
rtfm github-app create           # once: registers the GitHub App, writes ~/.config/rtfm/service.env
rtfm serve --env-file ~/.config/rtfm/service.env   # API + a worker on :8080
# then open http://localhost:3000/login
```

Without `RTFM_API_URL`, the dashboard shows read-only sample data (`src/lib/sample-api.ts`).
