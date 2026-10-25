# Try Midnight locally in 10 minutes

This walkthrough brings up the whole Midnight control plane on your laptop with
Docker Compose, opens the dashboard, and deploys a Redis container twice: once
through the dashboard and once through the CLI. It ends with tearing everything
down again.

It does not give you public HTTPS or a GitHub-connected service; see
[What you can't do yet locally](#what-you-cant-do-yet-locally) at the end.

For the full list of ports, services, and configuration knobs behind this
stack, see [`docs/dev-local-stack.md`](dev-local-stack.md).

## Prerequisites

- Docker Desktop (macOS/Windows) or Docker Engine (Linux) with the Compose
  plugin. `docker compose version` should work.
- Go 1.26.10 (the exact patch version pinned by `go.mod` and the Compose stack).
- Node.js 20 or newer and npm 10 or newer, to build the dashboard.
- Free localhost ports 8080, 15432, 15000, 11234, 8081, 8443, and 8082.
- `git` and `curl`.

## 1. Start the stack

From the repository root:

```sh
make dev-up
```

This starts Postgres, a local OCI registry, BuildKit, Traefik, the API server
(`http://localhost:8080`), the deploy worker, and the build worker, and applies
all database migrations.

The first run is slow. The Go services compile from source inside the
containers, and the deploy worker installs `docker.io` with `apt-get` on every
fresh start. Expect several minutes; `make dev-logs` shows progress.

Check that everything came up:

```sh
make dev-doctor ARGS=--live
```

## 2. Build the dashboard

The API serves the built dashboard at `http://localhost:8080/` whenever
`web-next/dist` exists. Build it once:

```sh
make web-build      # same as: cd web-next && npm ci && npm run build
```

You do not need to restart the stack; the API picks up the directory on the
next request.

## 3. Log in

Open <http://localhost:8080/>.

The login page asks for an email and password. There are no users yet, so
instead expand **Emergency recovery** at the bottom of the form, paste
`dev-secret`, and continue.

`dev-secret` is the local stack's operator recovery token
(`MIDNIGHT_API_BEARER_TOKEN`). It is installation-wide access meant for
recovery, not a normal user account. It can create projects and services and
deploy digest-pinned container images, which is enough for this walkthrough.
It cannot connect a GitHub repository; that needs a human workspace session.

### Create the first human user

The recovery token cannot connect a GitHub repository, so create a real
account before you get to the source-deploy step. There are two supported
paths, and both spend the same one-shot claim.

**From the console.** Paste `dev-secret` into **Emergency recovery** on the
login page. Because this installation has no administrator yet, the console
sends you to **Create your administrator account** instead of the project
list. Fill in the form, save the TOTP secret it shows you, enter the current
six-digit code, and it signs you in as the new user.

**From the CLI.** One command, run on the host:

```sh
MIDNIGHT_API_BASE=http://localhost:8080 MIDNIGHT_TOKEN=dev-secret \
  midnight admin create-user \
  --email you@example.com \
  --display-name "You" \
  --workspace-slug demo \
  --workspace-name "Demo"
```

It prompts for the password (at least 12 characters), generates the TOTP
secret, and prints the `otpauth://` enrollment URL and the raw Base32 secret
once. Save them, add the account to your authenticator, then run
`midnight login`. Add `--output json` for a machine-readable result.

Either path is one-shot: a second attempt reports that the installation
already has an administrator.

<details>
<summary>Raw API call (equivalent to the two paths above)</summary>

The claim needs a TOTP secret and a code that is valid right now. The snippet
below uses only the Python standard library (no `pyotp` needed). If you have
`oathtool` installed you can use `oathtool --totp -b "$TOTP_SECRET"` instead of
the code line.

```sh
eval "$(python3 - <<'EOF'
import base64, hashlib, hmac, os, struct, time
secret = base64.b32encode(os.urandom(20)).decode()
counter = struct.pack(">Q", int(time.time()) // 30)
digest = hmac.new(base64.b32decode(secret), counter, hashlib.sha1).digest()
offset = digest[-1] & 0x0F
code = (struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF) % 1000000
print(f"TOTP_SECRET={secret}")
print(f"TOTP_CODE={code:06d}")
EOF
)"
echo "Save this TOTP secret in your authenticator app: $TOTP_SECRET"

curl -sS -X POST http://localhost:8080/api/v1alpha/auth/claim \
  -H 'Authorization: Bearer dev-secret' \
  -H "Idempotency-Key: claim-$(date +%s)" \
  -H 'Content-Type: application/json' \
  -d "$(cat <<EOF
{
  "email": "you@example.com",
  "display_name": "You",
  "password": "change-this-long-password",
  "totp_secret": "$TOTP_SECRET",
  "totp_code": "$TOTP_CODE",
  "workspace_slug": "demo",
  "workspace_display_name": "Demo"
}
EOF
)"
```

The password must be at least 12 characters. Run the whole block at once so
the code is still within its 30-second window when the request lands. Then
sign in on the login page with the email, password, and a fresh code from your
authenticator app.

</details>

## 4. Create a project and deploy Redis from the dashboard

1. Click **Create a project**, keep the default workspace, give it a name such
   as `demo`, and create it. Every project gets a `production` environment.
2. On the project canvas click **+ Add resource** and choose **Container
   image**.
3. Name the service `redis` and paste this image reference:

   ```text
   redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e
   ```

   Only digest-pinned references are accepted, and the local stack trusts just
   the four official database images listed in `MIDNIGHT_WORKER_TRUSTED_IMAGES`
   in `docker-compose.local.yml` (MongoDB, MySQL, PostgreSQL, Redis) plus
   images pushed to its own registry. Tag-only references are rejected.
4. Deploy. The deployment appears on the canvas and under **Deployments**; open
   it to follow the timeline and logs. `docker ps` on your host shows the Redis
   container once the deploy worker has started it. The container port is read
   from the image's single `EXPOSE` when you have not configured one.

## 5. Do the same thing from the CLI

Build the CLI and point it at the local stack:

```sh
go build ./cmd/midnight
export MIDNIGHT_API_BASE=http://localhost:8080
export MIDNIGHT_TOKEN=dev-secret
```

Create a project, a service, and a deployment. Each command prints the ID the
next one needs.

```sh
./midnight project create --workspace-id wrk_default --name Demo demo-cli
./midnight environment list --project-id <prj_...>          # note the production env id
./midnight service create --project-id <prj_...> --name web web
./midnight deploy image \
  --service-id <svc_...> \
  --environment-id <env_...> \
  --image redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e
```

Watch it run:

```sh
./midnight deploy status --operation-id <op_...>
./midnight deploy logs   --operation-id <op_...>
```

The same deployment shows up in the dashboard under the `demo-cli` project.

## 6. Optional: deploy from source

The repository ships a tiny Node app that exercises the BuildKit build path.
Link a directory to a service and push it:

```sh
cd examples/build-proof/repo
../../../midnight link --project demo --environment production --service web
../../../midnight up
```

`midnight up` uploads the directory, queues a build, and then a deploy. Follow
it with `midnight deploy status` and `midnight deploy logs` as above. Source
builds need the build container to reach GitHub Container Registry to pull the
Railpack frontend image.

## 7. Tear down

```sh
make dev-down
```

This stops the compose services. Deployed app containers started by the deploy
worker run on your host Docker Engine, so remove any leftovers with
`docker ps` and `docker rm -f`. Database, registry, and BuildKit state live in
named volumes and survive `dev-down`; add `docker compose -f
docker-compose.local.yml down -v` to delete them.

## What you can't do yet locally

- **Public HTTPS.** Traefik runs on `localhost:8081`/`8443` and routes are
  published to it, but there is no public DNS, no firewall/NAT, and no ACME
  issuance in the local stack. Generated hostnames end in `apps.localhost`.
- **GitHub source without a human session.** The recovery token cannot link a
  GitHub repository to a service. Create a human user (step 3), configure a
  GitHub App, and sign in as that user for push-to-deploy.
- **Images outside the trusted list.** Any image not in
  `MIDNIGHT_WORKER_TRUSTED_IMAGES` and not pushed to the local registry is
  refused. Override the variable before `make dev-up` if you need others.
- **Multi-host.** The local stack is the single-host profile only.

## Next: install on a VM

To run Midnight for real on one Linux VM with the packaged services, follow
[`docs/install/operator-install-path.md`](install/operator-install-path.md),
then the [live deploy check](runtime/live-deploy-proof.md). The current
end-to-end verdict is in [`docs/deployment-readiness.md`](deployment-readiness.md).
