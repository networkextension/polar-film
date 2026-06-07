# polar-film

A **video / film knowledge base** for the Polar platform — a 豆瓣-style metadata
DB + AI retrieval layer, not a video site. It stores the *knowledge structure*
of films (metadata, segmented subtitles, screenshots, people, timeline, tags,
and — later — multi-level embeddings), **not the video files**: binaries live in
polar-assets, so storage is orders of magnitude cheaper while AI retrieval value
is higher. Plan & design: [`doc/dev-plan.md`](doc/dev-plan.md),
[`doc/design.md`](doc/design.md).

Like every Polar module it owns its own database (`polar_film`), validates user
sessions through dock (`polar-sdk`), and heartbeats into dock's plugin registry.
Status: **M0** — platform skeleton (no movie/subtitle/AI logic yet).

## Run (local)

```bash
createdb -O polar polar_film
psql -d polar_film -f scripts/migrate/film-schema.sql      # run as the polar role

POLAR_PLUGIN_TOKEN=<plaintext from dock /admin-plugins.html> \
POLAR_FILM_DB_DSN="postgres://polar:<pw>@127.0.0.1:5432/polar_film?sslmode=disable" \
POLAR_DOCK_BASE="http://127.0.0.1:8080" \
make run

curl -s localhost:8102/healthz
```

## Env vars

| var | default | notes |
|---|---|---|
| `POLAR_PLUGIN_TOKEN` | — | **required**; plaintext from dock admin (fatal if unset) |
| `POLAR_FILM_DB_DSN` | `…/polar_film?sslmode=disable` | connects only to `polar_film` |
| `POLAR_DOCK_BASE` | `http://127.0.0.1:8080` | dock `/internal/v1/*` base |
| `POLAR_PLUGIN_NAME` | `film` | must match `plugin_modules.name` in dock |
| `POLAR_FILM_LISTEN` | `127.0.0.1:8102` | |
| `POLAR_FILM_VERSION` | `0.0.1` | surfaced on heartbeat + `/healthz` |
| `POLAR_FILM_METRICS_TOKEN` | — | Bearer for `/metrics` (unset → 404) |
| `POLAR_FILM_PUBLIC_BASE_URL` | — | origin for the dock `/api/nav` sidebar link |

## Build / test

```bash
make tidy && make build && make vet
```

> pgvector is **not** required for M0–M3. Embeddings + semantic search arrive in
> M4 (which adds `CREATE EXTENSION vector` + embedding columns; install pgvector
> in the target Postgres first).
