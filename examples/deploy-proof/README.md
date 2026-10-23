# Deploy Proof Sample

This is a tiny deterministic HTTP app for proving the Midnight worker
Docker-to-Traefik path with an operator-owned sample image.

The app listens on `:8080` and serves fixed responses:

- `GET /` returns `midnight deploy proof`
- `GET /healthz` returns JSON suitable for Docker health checks

## Build

```sh
docker build -t midnight/deploy-proof:local examples/deploy-proof
```

## Run Directly

```sh
docker run --rm --name midnight-deploy-proof -p 8080:8080 midnight/deploy-proof:local
```

Verify the local container:

```sh
curl -fsS http://127.0.0.1:8080/
curl -fsS http://127.0.0.1:8080/healthz
docker inspect --format '{{.State.Health.Status}}' midnight-deploy-proof
```

## Worker/Traefik Proof Notes

Use image `midnight/deploy-proof:local` with internal/target port `8080`.
The container image includes a Docker `HEALTHCHECK` against `/healthz`, so the
Docker runtime should report `healthy` before the worker promotes the route.

For a route proof, configure the worker route target port as `8080` and publish
the route through the normal Traefik file-provider directory. A successful proof
returns the fixed body from `/` and the health JSON from `/healthz` through the
public Traefik route.
