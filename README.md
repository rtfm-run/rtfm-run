# front

Landing page for [vanguard.run](https://vanguard.run): an agent that follows your quickstart the way a new developer would, finds every step that breaks, writes the fix, and proves the fix works.

```bash
npm install
npm run dev   # http://localhost:3000
```

Next.js (App Router) + TypeScript. The page is `src/app/page.tsx`; the hero replay is `src/app/run-replay.tsx`.

## Connecting the API

The dashboard talks to the vanguard API from the browser. Set `VANGUARD_API_URL` on the frontend (read at request time, no rebuild needed):

```bash
VANGUARD_API_URL=http://localhost:8080 npm run dev
```

On the API, set `VANGUARD_DASHBOARD_URL` to this app's `/dashboard` URL. Sign-in uses a session cookie, so the frontend and the API must be on the same site (for example `vanguard.run` and `api.vanguard.run`, or two ports on `localhost`).

Without `VANGUARD_API_URL` the dashboard shows read-only sample data.
