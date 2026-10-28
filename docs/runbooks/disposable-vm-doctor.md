# Disposable-VM Doctor Harness

Operator-facing runbook for the `midnight doctor` disposable-VM CI
harness. This page closes Blocker (1) of the `v0.1.0-alpha`
greenlight brief
(`agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md` §4) by
documenting how to:

1. Reproduce the GitHub Actions run locally on a disposable VM.
2. Interpret the JUnit XML / doctor JSON artifacts the workflow
   uploads.
3. Understand which checks are proven by the current harness and
   which package-boot checks still require operator review.

## What the harness does

The two workflows
(`.github/workflows/doctor-vm-ec1.yml`,
`.github/workflows/doctor-vm-ec2.yml`) implement the panel-locked
exit criteria 0.1-EC1 (Ubuntu 24.04) and 0.1-EC2 (Debian 12) from
`agile/RELEASE_PLAN.md` §2.1:

> 0.1-EC1 — Fresh Ubuntu 24.04 VM → `midnight doctor` passes in <10 min
> 0.1-EC2 — Fresh Debian 12 VM → `midnight doctor` passes in <10 min

Each workflow:

1. Builds the unsigned `.deb` via `make package-deb`.
2. Installs it on a disposable host (Ubuntu 24.04 GitHub-hosted
   runner; or a `debian:12-slim` Docker container with
   `--privileged --cgroupns=host`).
3. Runs `tools/vm-harness` against `midnight-doctor --json`.
4. Asserts the alpha-floor host preflight contract:
   - exit code `0` (panel-locked, `doctor.ExitCodeForResults`).
   - total wall-clock duration < `--max-duration` (default 10m).
   - every `OR-CHK-DOC-001..009` entry reports `status="pass"`.
5. Uploads `doctor.json` + `junit.xml` (+ failure diagnostics) as a
   14-day GitHub Actions artifact.

The live doctor registry also includes `OR-CHK-DOC-010..013` for the
package boot path: Postgres DSN, cursor signing key, local registry
config, and external runtime binaries. Those are not part of the
historical EC1/EC2 `001..009` assertion. Treat them as the design
partner service-enable checklist, not as proof supplied by the
disposable harness.

Two negative-path jobs run in the same matrix:

- `daemon-json-conflict`: pre-pollutes `/etc/docker/daemon.json`
  with a conflicting `storage-driver: devicemapper` and asserts
  doctor returns exit `2` with `OR-CHK-DOC-002` reporting the
  merged-diff (the panel-locked 0.1-EC4 contract).
- `cgroup-v1-fixture`: drives the cgroup-v1 refusal through the
  `--hostsource=fixture:internal/doctor/testdata/cgroup-v1` seam in
  `cmd/midnight-doctor` and asserts `OR-CHK-DOC-001` reports
  `status="fail"` with the panel-locked
  `add 'systemd.unified_cgroup_hierarchy=1' to the kernel command
  line and reboot` remediation (0.1-EC3).

## Reproducing locally

The harness binary is portable and the fixture seam is committed, so
the following local tools can reproduce the workflow shape. A true
Ubuntu 24.04 or Debian 12 VM gives the best signal; the Debian Docker
path below is the same lower-fidelity compromise used by CI.

### Vagrant

```sh
# Vagrantfile snippet
Vagrant.configure("2") do |config|
  config.vm.define "ubuntu" do |c|
    c.vm.box = "bento/ubuntu-24.04"
  end
  config.vm.define "debian" do |c|
    c.vm.box = "debian/bookworm64"  # debian 12
  end
end
```

```sh
vagrant up ubuntu
vagrant rsync ubuntu        # sync the repo
vagrant ssh ubuntu -- '
  cd /vagrant
  make package-deb
  sudo apt-get install -y ../midnight_*.deb
  docker version --format "{{.Server.APIVersion}}" | sudo tee /tmp/docker-version.txt >/dev/null
  ss -ltn | sudo tee /tmp/ss-ltn.txt >/dev/null
  go build -o ./vm-harness ./tools/vm-harness
  ./vm-harness \
    --doctor-binary=midnight-doctor \
    --distro=ubuntu-24.04 \
    --max-duration=10m \
    --json-out=./artifacts/doctor.json \
    --junit-out=./artifacts/junit.xml \
    --doctor-args=--mountinfo-fixture=/proc/self/mountinfo,--daemon-json-fixture=/etc/docker/daemon.json,--docker-version-fixture=/tmp/docker-version.txt,--ss-ltn-fixture=/tmp/ss-ltn.txt,--firewall-mode=none,--mac-mode=apparmor-enforce,--time-sync=synchronized,--hostname-fqdn=ec1.midnight.local,--docker-packages=docker-ce
'
```

### multipass (Ubuntu)

```sh
multipass launch 24.04 --name ec1 --cpus 2 --memory 2G --disk 10G
multipass transfer midnight ec1:/home/ubuntu/midnight
multipass exec ec1 -- bash -c 'cd ~/midnight && make package-deb && \
    sudo apt-get install -y ../midnight_*.deb && \
    go build -o /tmp/vm-harness ./tools/vm-harness && \
    /tmp/vm-harness --doctor-binary=midnight-doctor --distro=ubuntu-24.04 \
        --max-duration=10m --json-out=./doctor.json --junit-out=./junit.xml \
        --doctor-args=--mountinfo-fixture=/proc/self/mountinfo,--daemon-json-fixture=/etc/docker/daemon.json,--firewall-mode=none,--mac-mode=apparmor-enforce,--time-sync=synchronized,--hostname-fqdn=ec1.midnight.local,--docker-packages=docker-ce'
multipass delete ec1 && multipass purge
```

### lima (macOS / Linux)

```sh
limactl start --name ec2 template://debian-12
limactl shell ec2 -- bash -c 'cd $HOME/midnight && \
    make package-deb && sudo apt-get install -y ../midnight_*.deb && \
    go build -o /tmp/vm-harness ./tools/vm-harness && \
    /tmp/vm-harness ... '
limactl stop ec2 && limactl delete ec2
```

### Docker (no VM, lower fidelity)

```sh
docker run --rm \
  --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup \
  -v "$PWD":/work -w /work \
  debian:12-slim \
  bash ./.github/scripts/doctor-vm-ec2-positive.sh
```

The Docker path is exactly what `.github/workflows/doctor-vm-ec2.yml`
runs in CI; it shares the runner kernel so cgroup-v1 refusal is
exercised through the fixture seam, not on real cgroup-v1 hardware.

## Design partner service-enable checklist

After installing the `.deb` on Ubuntu 24.04 or Debian 12, review
`/var/lib/midnight/doctor-postinst.json` first. The postinst doctor run
does not enable services. It executes the live `install-readiness` profile and
labels that scope in JSON; host checks are outside this report and must be run
with captured facts before enablement.

Before running `systemctl enable --now ...`, capture the host facts and
rerun doctor explicitly:

```sh
docker version --format '{{.Server.APIVersion}}' \
  | sudo tee /tmp/midnight-docker-api.txt >/dev/null
ss -ltn | sudo tee /tmp/midnight-ss-ltn.txt >/dev/null

sudo midnight-doctor --json --live \
  --mountinfo-fixture=/proc/self/mountinfo \
  --daemon-json-fixture=/etc/docker/daemon.json \
  --docker-version-fixture=/tmp/midnight-docker-api.txt \
  --ss-ltn-fixture=/tmp/midnight-ss-ltn.txt \
  --firewall-mode=none \
  --mac-mode=apparmor-enforce \
  --time-sync=synchronized \
  --hostname-fqdn="$(hostname -f)" \
  --docker-packages=docker-ce
```

The `--live` flag wires fixed read-only cgroup/Docker version commands with
three-second deadlines and uses default package paths for `OR-CHK-DOC-010..013`;
the example above supplies captured Docker API and ingress-listener data.
Without a Docker version file, live mode runs that read probe automatically.
Then verify the package-boot inputs that are outside
the disposable harness floor:

- `OR-CHK-DOC-010`: configure `MIDNIGHT_DB_DSN` or
  `/etc/midnight/db.dsn`.
- `OR-CHK-DOC-011`: confirm the generated cursor key exists and is not
  empty.
- `OR-CHK-DOC-012`: confirm `/etc/midnight/registry.yml` exists.
- `OR-CHK-DOC-013`: install pinned `buildkitd`, `buildctl`, and
  `registry` binaries or set the `MIDNIGHT_*` binary paths in
  `/etc/midnight/midnight.env`.

Interpretation is strict: `fail` blocks service enablement, `warn`
requires an operator decision, and `skip` means the run did not prove
that check. The current documentation does not claim a completed live
Debian 12 VM verification; CI uses a privileged Debian 12 container and
tracks true-VM promotion separately.

## Interpreting the artifacts

Each workflow run uploads two files per matrix entry (`positive`,
`daemon-json-conflict`, `cgroup-v1-fixture`):

### `doctor.json`

Verbatim output of `midnight-doctor --json`. Schema:

```json
{
  "schema_version": 1,
  "live": true,
  "profile": "all",
  "exit_code": 0,
  "results": [
    {"id":"OR-CHK-DOC-001","description":"...","status":"pass","message":"..."},
    ...
  ]
}
```

Pipe to `jq` for human reading:

```sh
jq -r '.results[] | "\(.status)\t\(.id)\t\(.message)"' doctor.json
```

### `junit.xml`

JUnit Surefire-flavored test suite emitted by `tools/vm-harness`.
One `<testcase>` per `OR-CHK-DOC` id plus two synthetic cases
`vm-harness.duration` and `vm-harness.exit-code`. Failed cases
include the panel-locked failure message in `<failure>` so
downstream rollups (e.g. `release-evidence/`) can quote them
verbatim without re-parsing the doctor JSON.

### Failure diagnostics

If any matrix entry fails, the workflow uploads
`artifacts/doctor-vm-{ec1,ec2}/diagnostics/` containing:

- `docker.journal` — `journalctl -u docker --no-pager`.
- `daemon.json`    — copy of `/etc/docker/daemon.json` post-postinst.
- `doctor-postinst.json` — copy of the doctor JSON the postinst
  emitted on first install.
- `mountinfo.txt`  — `/proc/self/mountinfo` from the failing host.
- `uname.txt`      — kernel + arch.

These are sufficient to reconstruct the failing host's preflight
fact set without remoting in.

## Strategy choice for 0.1-EC2 (Debian 12)

GitHub-hosted runners do **not** offer a `debian-12` image. We
evaluated three strategies in the workflow header and chose:

> **(i)** `runs-on: ubuntu-24.04` + a `debian:12-slim` Docker
> container with `--privileged --cgroupns=host -v
> /sys/fs/cgroup:/sys/fs/cgroup`.

Pros:
- Zero infra to provision; alpha greenlight floor is clean.
- Cold start is ~30s (image pull cached); positive-path matrix
  entry typically finishes in 3–5 minutes against the 10-minute
  budget.

Cons / known limitations (tracked in
`phase1-blocker1-vm-ci-followup`):
- Container is not a true VM. The kernel is the runner's Linux
  6.x with cgroup v2 unified hierarchy, so cgroup-v1 refusal is
  exercised through the fixture seam rather than on real v1
  hardware.
- The `.deb` postinst runs `systemctl daemon-reload` which is a
  no-op without systemd PID 1 (the postinst tolerates this with
  `|| true`). Service-activation smoke is out of scope for the
  alpha-floor harness.
- We synthesize a `docker version` fixture (`1.45`) so
  `OR-CHK-DOC-003` has something to read; the live-dockerd smoke
  is targeted for `v0.2.0-alpha`.

The strategy upgrade path is to either lift the harness onto a
self-hosted Hetzner / DigitalOcean / Equinix runner spinning a real
Debian 12 cloud-init image (strategy iii), or invoke `lima` /
`qemu` from a GitHub-hosted runner (strategy ii). Both are tracked
in `phase1-blocker1-vm-ci-followup` and slated for the
`v0.2.0-alpha` greenlight.

## Adding a new fixture

Drop a directory under `internal/doctor/testdata/` with the
canonical filenames documented in
`internal/doctor/testdata/README.md`:

| File | Doctor check it feeds |
|---|---|
| `mountinfo` | `OR-CHK-DOC-001` |
| `daemon.json` | `OR-CHK-DOC-002` |
| `docker-version.txt` | `OR-CHK-DOC-003` |
| `ss-ltn.txt` | `OR-CHK-DOC-004` |
| `hints.json` | `OR-CHK-DOC-005..009` |

Then point the harness at it via
`--hostsource=fixture:internal/doctor/testdata/<name>`. No CLI flag
plumbing changes are required; the seam is generic.
