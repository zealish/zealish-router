# Zealish Router dashboard

Next.js dashboard for [Zealish Router](../../README.md): Overview, Models,
Providers, API Keys and Settings.

It is a pure client of the admin REST API at `/api/v1`. No model traffic passes
through Next.js.

## Running

```sh
cp .env.example .env.local     # NEXT_PUBLIC_ROUTER_URL, defaults to :8787
npm install
npm run dev                    # http://localhost:3000
```

The router must run with `admin.enabled: true` and list the dashboard origin
under `admin.cors_origins`. The admin token is entered in the browser and kept
in `localStorage` — it is never part of the build.

## Checks

```sh
npx eslint .
npm run build
```
