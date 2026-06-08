# polar-film — deployment (dev: macOS arm64 home server)

End-to-end recipe for running film-svc on the dev box (`10.88.0.5`, macOS
arm64, Homebrew). Production is the same shape on Linux (systemd instead of
launchd, distro pgvector/ollama packages). Ports: dns 8101, **film 8102**,
stock 8103, assets 8104, ollama 11434.

## 1. Postgres + pgvector

film owns its own DB `polar_film` (per database-ownership). dev runs
**postgresql@16**.

```bash
# DB owned by the polar role
psql -d postgres -c "CREATE DATABASE polar_film OWNER polar;"
psql -d polar_film -c "ALTER SCHEMA public OWNER TO polar; GRANT ALL ON SCHEMA public TO polar;"

# base + vector + generalize schema (idempotent; run as polar)
PGPASSWORD=$PW psql -U polar -h 127.0.0.1 -d polar_film -f scripts/migrate/film-schema.sql
PGPASSWORD=$PW psql -U polar -h 127.0.0.1 -d polar_film -f scripts/migrate/film-schema-m4.sql
PGPASSWORD=$PW psql -U polar -h 127.0.0.1 -d polar_film -f scripts/migrate/film-schema-m7.sql
```

**pgvector gotcha**: the Homebrew `pgvector` bottle ships only pg17/18 binaries
— useless for pg16. Build from source against pg16, and create the extension
as a **superuser** (the `polar` role can't `CREATE EXTENSION`):

```bash
git clone --depth 1 --branch v0.8.2 https://github.com/pgvector/pgvector.git
cd pgvector && make install PG_CONFIG=/opt/homebrew/opt/postgresql@16/bin/pg_config
psql -d polar_film -c "CREATE EXTENSION IF NOT EXISTS vector;"   # as default superuser
```
⚠️ A `brew upgrade postgresql@16` wipes the source-built `vector.dylib` — rebuild.

## 2. Embedding backend (ollama bge-m3)

```bash
ollama pull bge-m3            # 1024-dim, Chinese-strong; ~1.1G into ~/.ollama
```
**ollama gotcha**: the Homebrew `ollama` bottle (0.30.6) bundles only the MLX
runner, no `llama-server` → GGUF models fail with "llama-server binary not
found". Use the **official tarball** which bundles all runners:

```bash
cd ~/.local/ollama-dist
curl -fL -o o.tgz https://github.com/ollama/ollama/releases/download/v0.30.6/ollama-darwin.tgz
tar xzf o.tgz && rm o.tgz
# launchd com.polar.ollama runs:  ~/.local/ollama-dist/ollama serve  (OLLAMA_HOST=127.0.0.1:11434)
```
Other backends (DashScope / OpenAI / a future dock endpoint) need no code
change — just point `POLAR_FILM_EMBED_BASE_URL` at their `/v1` and set the key.

## 3. Register the plugin in dock

```sql
-- plugin_key_hash = hex(sha256(plaintext token))
INSERT INTO plugin_modules (name, display_name, enabled, endpoint, version, plugin_key_hash, config)
VALUES ('film','Film Knowledge Base', true, 'http://127.0.0.1:8102', '0.7.0-m7', '<HASH>', '{}'::jsonb)
ON CONFLICT (name) DO UPDATE SET endpoint=EXCLUDED.endpoint, plugin_key_hash=EXCLUDED.plugin_key_hash,
  enabled=true, version=EXCLUDED.version;
```

## 4. Env (`~/.config/polar/film-svc.env`, 0600)

```
POLAR_FILM_DB_DSN=postgres://polar:<pw>@127.0.0.1:5432/polar_film?sslmode=disable
POLAR_DOCK_BASE=http://127.0.0.1:8080
POLAR_PLUGIN_NAME=film
POLAR_PLUGIN_TOKEN=<plaintext token>
POLAR_FILM_LISTEN=127.0.0.1:8102
POLAR_FILM_VERSION=0.7.0-m7
POLAR_FILM_PUBLIC_BASE_URL=https://film.dev.4950.store
POLAR_FILM_EMBED_BASE_URL=http://127.0.0.1:11434/v1
POLAR_FILM_EMBED_MODEL=bge-m3
POLAR_FILM_EMBED_DIM=1024
# POLAR_FILM_EMBED_API_KEY=        # set for DashScope/OpenAI; ollama ignores
# POLAR_FILM_METRICS_TOKEN=        # set to expose /metrics (unset → 404)
```

## 5. Binary + launchd

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o ~/.local/bin/film-svc ./cmd/film-svc
# launchd com.polar.film-svc runs:  bash -lc 'set -a; source film-svc.env; exec ~/.local/bin/film-svc'
launchctl load ~/Library/LaunchAgents/com.polar.film-svc.plist
curl -s 127.0.0.1:8102/healthz       # {"db_ok":true,...,"version":"0.7.0-m7"}
```

## 6. nginx subdomain `film.dev.4950.store`

Block in `plugins-dev.conf` (mirrors the dns block):

```nginx
location ~ ^/api/film(/|$) { proxy_pass http://127.0.0.1:8102; ... }   # film app API
location /healthz { proxy_pass http://127.0.0.1:8102; }
location /metrics { proxy_pass http://127.0.0.1:8102; }
location /api/    { proxy_pass http://127.0.0.1:8080; ... }            # dock: /api/me,/api/teams,/api/llm-configs
location /        { proxy_pass http://127.0.0.1:8102; }                # film-svc serves its own UI
```
`*.dev.4950.store` is a wildcard A record → 10.88.0.5. **The nginx master runs
as root** → reload with `sudo -n nginx -s reload` (dev has passwordless sudo).

## 7. Smoke test

```bash
curl -sk https://film.dev.4950.store/healthz
curl -sk https://film.dev.4950.store/ | grep -o "Film Knowledge Base"   # UI served
```
Then log in at dock so the `.4950.store` `access_token` cookie is set, and open
https://film.dev.4950.store. Grant the workspace film access (dock admin, or
`workspace_plugin_access` upsert) — the gate is closed-by-default.

## Ops: purge a workspace's film data

```bash
curl -s 127.0.0.1:8102/internal/v1/film/workspace-deleted \
  -H 'Content-Type: application/json' -d '{"workspace_id":"<ws>"}'
```
Loopback-only. (dock has no workspace-deletion fan-out yet; this is ready for
it and usable as a manual purge.)
