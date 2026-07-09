> ⚠️ **Status: untested.** This extension is provided as-is and has **not been tested in production**. Please feel free to fork, modify, improve, and open pull requests.
>
> Licensed under **GNU GPLv3** (see [LICENSE](LICENSE)).

# IP Block for Traefik v3

A Traefik v3 middleware plugin (Go, Yaegi-interpreted) that consults the
[ip-block.com](https://www.ip-block.com) decision API and blocks matching client
IPs, with a per-IP decision cache.

- **Tested against:** Traefik **v3.7.x** (latest stable, 2026).
- **Type:** middleware plugin (no compilation step — Traefik interprets the Go
  source at runtime via Yaegi; the plugin uses only the standard library).

## Files

| File | Purpose |
|------|---------|
| `ipblock.go` | The plugin: `CreateConfig`, `New`, and the `http.Handler`. |
| `.traefik.yml` | Plugin manifest (required by Traefik's plugin catalog/loader). |
| `go.mod` | Module definition (stdlib only). |
| `traefik-static.example.yml` | Static config registering the plugin. |
| `dynamic-config.example.yml` | Dynamic config defining + using the middleware. |

## Install

### Option A — published plugin (Traefik Pilot / GitHub)

1. Push this folder to a public repo at the module path
   `github.com/ip-block/traefik-ipblock` and tag a release (e.g. `v1.0.0`).
2. Register it in your **static** config (see `traefik-static.example.yml`):
   ```yaml
   experimental:
     plugins:
       ipblock:
         moduleName: github.com/ip-block/traefik-ipblock
         version: v1.0.0
   ```

### Option B — local plugin (no publishing)

1. Place the source at
   `./plugins-local/src/github.com/ip-block/traefik-ipblock/` (next to the Traefik
   binary/working dir).
2. Register with `localPlugins` (see the commented block in the static example).

### Then, in your dynamic config

Define the middleware and attach it to a router (see
`dynamic-config.example.yml`):

```yaml
http:
  middlewares:
    ip-block:
      plugin:
        ipblock:
          siteId: your-site-id
          apiKey: your-api-key
  routers:
    my-app:
      middlewares: [ip-block]
```

Restart Traefik so it downloads/loads the plugin.

## Configuration

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `true` | Master switch. |
| `siteId` | — (required) | Site id. |
| `apiKey` | — (required) | API key (JSON body). |
| `apiUrl` | `https://api.ip-block.com/v1/check` | Endpoint. |
| `failOpen` | `true` | Allow on error/timeout; `false` fails closed. |
| `cacheTtl` | `300` | Per-IP cache seconds (`0` disables). |
| `timeoutMs` | `1000` | API timeout. |
| `behindProxy` | `false` | Read client IP from `realIpHeader`. |
| `realIpHeader` | `X-Forwarded-For` | Header used when `behindProxy`. |
| `blockAction` | `403` | `403` or `redirect`. |
| `blockRedirectUrl` | `https://www.ip-block.com/blocked.php` | Redirect target. |
| `blockMessage` | `Access denied.` | 403 body. |
| `whitelist` | `[]` | IPs never checked. |

## Behaviour

- Blocks only on `{"action":"block"}`; otherwise allows (subject to `failOpen`).
- API errors are not cached; the next request retries. `allow`/`block` decisions are
  cached for `cacheTtl`.
- Whitelisted IPs short-circuit before the cache/API.
- The 1s budget is enforced on the `http.Client` and a request-context deadline.

## Notes

- Because the plugin is Yaegi-interpreted, it deliberately avoids third-party
  imports and generics.
- When behind a proxy, prefer Traefik's own trusted-IP / forwarded-headers handling;
  `behindProxy` + `realIpHeader` are a direct-header fallback.
- The cache is per Traefik process (in-memory map guarded by a mutex).
