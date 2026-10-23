# ADR-016: Multi-Host Dataplane (Phase 3 Design Drafted in Phase 1)

- Status: Accepted (Parity Review) — Phase 1 design draft, Phase 3 GA
- Date: 2026-04-25
- Deciders: Parity Review (Reviewer 3 G-01 top must-fix)
- Tags: [networking, phase-1, phase-3, dataplane, wireguard]

## Context

The spec's Phase 3 multi-host networking is a single paragraph naming WireGuard, IPAM, DNS, firewall policy, key rotation, and MTU management. By the time Phase 3 starts, the team would face writing the design from scratch — a guaranteed slip. The Parity Reviewer rated this top-3 must-fix because every Phase 3 promise (multi-host, Kubernetes adapter, replicas, regions) depends on the dataplane being designed correctly.

Spec references:

- `Networking Plane / Multi-host networking` (~L1010-L1015)
- `Parity Review Locked Decisions` → Multi-host dataplane (Phase 1 design draft)
- Phase 3 in Roadmap.

## Decision

The multi-host dataplane design is **drafted in Phase 1** even though delivery is Phase 3. The drafted ADR covers:

1. **WireGuard mesh peer registry**:
   - Each host runs a WireGuard endpoint authenticated by a control-plane-issued long-lived public key (rotation supported).
   - Peer registry is a Postgres table `wg_peers` with `host_id, public_key, endpoint, allowed_ips[], last_handshake_at`.
   - The control plane is the registry source-of-truth; agents poll/subscribe and reconcile their local WireGuard config.

2. **IPAM allocator**:
   - The dataplane reserves a `/16` from RFC1918 by default (configurable).
   - Per-(project, environment) gets a `/24` carved from the dataplane block.
   - Per-service-replica gets a `/30`.
   - Allocation conflicts during peer rejoin are **quarantined** (peer is admitted to the registry but not given allowed-ips until conflict resolves).

3. **Peer-discovery protocol**:
   - Agents register via the existing API token flow.
   - On registration, control plane returns the operator's WireGuard public key + endpoint plus allowed-ips for the new peer's `/24`.
   - Other peers learn of the new peer via the watch stream on `wg_peers` and add the new peer to their local config.

4. **MTU management**:
   - Default MTU is **1380** (well under the typical 1500-byte path MTU minus WireGuard overhead 80 bytes).
   - PMTU probe runs at peer-up: each agent sends DF-set packets at descending MTU sizes to every other peer, records the working MTU per (src, dst), and updates routing accordingly.
   - Per-link override available for hosts behind tunnels with smaller MTUs (e.g., 1280 minimum for IPv6).

5. **Key rotation with overlap window**:
   - WireGuard keys rotate every 90 days.
   - During a 7-day overlap window, both old and new public keys are accepted; agents update their peer's public key when the new one's `last_handshake_at` proves activity.
   - Lost-key recovery: operator-initiated re-registration via `midnight host re-register --force-rotate`.

6. **Peer add/remove FSM**:
   - States: `PROPOSED → REGISTERED → CONNECTED → DRAINING → REMOVED`.
   - `DRAINING` quarantines IPAM until heartbeat-loss exceeds threshold, then releases.
   - On `REMOVED`, all peers flush conntrack entries with the removed peer's allowed-ips to avoid stale connections.

7. **NetworkPolicy primitive**:
   - Per-service `egress: {allow: [...], deny: [...]}` enforced via `nftables` rules in `MIDNIGHT-USER` chain.
   - Default policy: SMTP (25/465/587) blocked outbound (per panel security rule); RFC1918 / cloud metadata blocked outbound (per SSRF-safe policy).
   - Per-environment policies inherited.

8. **Multi-host integration test rig**:
   - 3-node Vagrant/Lima/Multipass cluster for CI.
   - Tests include: peer add/remove churn under load, key rotation mid-traffic, network partition recovery, MTU regression, IPAM conflict on rejoin, conntrack flush correctness.

## Alternatives Considered

- **Tailscale (managed) instead of self-hosted WireGuard.** Adds a hosted dependency that conflicts with self-hosted-first positioning; introduces a third-party trust requirement.
- **Cilium / eBPF-based mesh.** Far more powerful but kernel-version-sensitive (5.4+ required, 5.10 strongly preferred); adds operational complexity that fights the panel's "boring durable PaaS" goal. Reassess if Kubernetes adapter ships first.
- **Consul Connect / service mesh.** Operationally heavy; changes the trust model; HashiCorp licensing complicates the OSS story.
- **VXLAN over flat L2.** Requires multicast or BUM flooding; doesn't work across cloud providers; insecure on shared L2.
- **Native WireGuard kernel module only (no userspace).** Restricts to recent kernels; fall back to wireguard-go for compatibility.

## Consequences

**Positive**: Phase 3 has a real design before sprint planning; risk visible early; Phase 1 implementation can land single-host primitives that compose with multi-host later.

**Negative**: design draft costs ~2 sprint-weeks in Phase 1 even though delivery is Phase 3; some design assumptions may need revision when Phase 3 starts.

**Follow-ups**:
- Phase 1: ship the ADR + `FEATURE_MULTIHOST_DATAPLANE.md` + a single-host stub of the WireGuard agent code (no peers).
- Phase 3: ship the full implementation + integration test rig + production failure drills.
- Phase 3 GA gate: pass all integration test rig drills + an external pen test of the dataplane.

## Compliance / Verification

- CI gate `gate.dataplane-design-stable` checks that the ADR + feature dossier exist and the spec lines referenced have not been silently mutated.
- In Phase 3: `gate.multihost-conformance` runs the integration test rig.
- Drill `DRILL-WG-KEY-ROTATION` (Phase 3) verifies the 7-day overlap window.
- Drill `DRILL-WG-PMTU-REGRESSION` (Phase 3) verifies path-MTU detection.
- Drill `DRILL-WG-PEER-CHURN` (Phase 3) runs 100 add/remove cycles under traffic.

## References

- Spec: `Networking Plane`, `Parity Review Locked Decisions`
- Feature dossier: `agile/features/FEATURE_MULTIHOST_DATAPLANE.md` (drafted Phase 1)
- Reviewer source: `agile/parity-review/PARITY_03_PLATFORM_INFRA.md` (G-01)
