#!/usr/bin/env python3
"""Validate the evidence contract emitted by an external parity runner."""

from __future__ import annotations

import datetime as dt
import hashlib
import json
import pathlib
import re
import sys


SCHEMA = "midnight.parity.runner.v1"
ASSERTION_SCHEMA = "midnight.parity.assertion.v1"
MAX_ARTIFACT_BYTES = 100 * 1024 * 1024
FUTURE_SKEW = dt.timedelta(minutes=2)
REQUIRED_ASSERTIONS = {
    "dataplane": {
        "fresh_install",
        "source_build",
        "image_deploy",
        "official_databases",
        "runtime_replacement",
        "redaction",
        "cleanup",
    },
    "fault": {
        "fresh_install",
        "process_death_matrix",
        "durable_recovery",
        "duplicate_suppression",
        "redaction",
        "cleanup",
    },
    "observability": {
        "fresh_install",
        "build_logs",
        "runtime_logs",
        "http_logs",
        "metrics",
        "notifications",
        "redaction",
        "cleanup",
    },
    "vm": {
        "fresh_install",
        "package_install",
        "source_deploy",
        "public_https",
        "restart_recovery",
        "upgrade_rollback",
        "backup_restore",
        "uninstall_cleanup",
        "redaction",
        "cleanup",
    },
    "restore": {
        "fresh_install",
        "off_host_backup",
        "source_destroyed",
        "restore",
        "application_probe",
        "redaction",
        "cleanup",
    },
}
REQUIRED_OBSERVATIONS = {
    "fresh_install": {"installation_id", "provisioned_at", "empty_state_verified"},
    "source_build": {"source_fixture", "image_digest", "probe_status"},
    "image_deploy": {"image_reference", "image_digest", "probe_status"},
    "official_databases": {"engines_tested", "engines_passed", "persistence_verified"},
    "runtime_replacement": {"replacements_tested", "replacements_passed", "persistence_verified"},
    "process_death_matrix": {"fault_points_total", "fault_points_passed", "max_recovery_seconds"},
    "durable_recovery": {"operations_recovered", "terminal_state_verified"},
    "duplicate_suppression": {"duplicates_injected", "duplicate_side_effects"},
    "build_logs": {"records_captured", "correlation_verified"},
    "runtime_logs": {"records_captured", "correlation_verified"},
    "http_logs": {"records_captured", "correlation_verified"},
    "metrics": {"samples_captured", "aggregation_verified"},
    "notifications": {"deliveries_attempted", "deliveries_confirmed"},
    "package_install": {"package_version", "services_healthy"},
    "source_deploy": {"source_fixture", "deployment_id", "probe_status"},
    "public_https": {"hostname", "status_code", "certificate_verified"},
    "restart_recovery": {"services_restarted", "application_probe_status"},
    "upgrade_rollback": {"upgrade_version", "rollback_version", "application_probe_status"},
    "backup_restore": {"backup_id", "restore_id", "application_probe_status"},
    "uninstall_cleanup": {"resources_created", "residual_resources"},
    "off_host_backup": {"backup_id", "artifact_count", "checksums_verified"},
    "source_destroyed": {"source_absent", "destruction_verified"},
    "restore": {"restore_id", "restored_artifact_count", "checksums_verified"},
    "application_probe": {"probe_url", "status_code", "content_verified"},
    "redaction": {"files_scanned", "credential_findings"},
    "cleanup": {"resources_created", "residual_resources"},
}
SECRET_VALUE = re.compile(
    r"(?:midn_(?:admin|agent|boot|change|mcp|root|sa|secret|user)_[A-Za-z0-9_-]+|Bearer\s+\S+|-----BEGIN [A-Z ]+PRIVATE KEY-----)",
    re.IGNORECASE,
)
SECRET_BYTES = re.compile(SECRET_VALUE.pattern.encode(), re.IGNORECASE)
SHA256 = re.compile(r"^[0-9a-f]{64}$")


def fail(message: str) -> None:
    raise SystemExit(f"parity runner evidence: {message}")


def timestamp(value: object, field: str) -> dt.datetime:
    if not isinstance(value, str) or not value.strip():
        fail(f"{field} must be a non-empty RFC3339 timestamp")
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        fail(f"{field} is not RFC3339")
    if parsed.tzinfo is None:
        fail(f"{field} must include a timezone")
    return parsed.astimezone(dt.timezone.utc)


def walk_strings(value: object):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for key, child in value.items():
            if isinstance(key, str):
                yield key
            yield from walk_strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk_strings(child)


def resolve_artifact(root: pathlib.Path, relative: object) -> pathlib.Path:
    if not isinstance(relative, str) or not relative.strip():
        fail("artifact path must be a non-empty relative path")
    candidate_path = pathlib.PurePosixPath(relative)
    if candidate_path.is_absolute() or ".." in candidate_path.parts:
        fail(f"artifact path escapes evidence root: {relative!r}")
    unresolved = root / pathlib.Path(*candidate_path.parts)
    if unresolved.is_symlink():
        fail(f"artifact must not be a symlink: {relative!r}")
    candidate = unresolved.resolve()
    try:
        candidate.relative_to(root)
    except ValueError:
        fail(f"artifact path escapes evidence root: {relative!r}")
    if not candidate.is_file():
        fail(f"artifact is missing or not regular: {relative!r}")
    return candidate


def hash_and_scan_artifact(path: pathlib.Path) -> tuple[str, int]:
    digest = hashlib.sha256()
    size = 0
    tail = b""
    with path.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            size += len(chunk)
            if size > MAX_ARTIFACT_BYTES:
                fail(f"artifact exceeds {MAX_ARTIFACT_BYTES} bytes: {path.name}")
            digest.update(chunk)
            scan = tail + chunk
            if SECRET_BYTES.search(scan):
                fail(f"artifact contains credential-like material: {path.name}")
            tail = scan[-256:]
    if size == 0:
        fail(f"artifact is empty: {path.name}")
    return digest.hexdigest(), size


def retained_file_set(root: pathlib.Path, manifest_path: pathlib.Path) -> set[str]:
    retained: set[str] = set()
    manifest_resolved = manifest_path.resolve()
    for candidate in root.rglob("*"):
        if candidate.is_symlink():
            fail(f"undeclared symlink exists in evidence root: {candidate.relative_to(root)}")
        if candidate.is_dir():
            continue
        if not candidate.is_file():
            fail(f"special file exists in evidence root: {candidate.relative_to(root)}")
        if candidate.resolve() == manifest_resolved:
            continue
        retained.add(candidate.relative_to(root).as_posix())
    return retained


def require_positive_integer(observations: dict[str, object], key: str) -> int:
    value = observations.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        fail(f"assertion observation {key!r} must be a positive integer")
    return value


def require_zero(observations: dict[str, object], key: str) -> None:
    value = observations.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or value != 0:
        fail(f"assertion observation {key!r} must be zero")


def require_true(observations: dict[str, object], key: str) -> None:
    if observations.get(key) is not True:
        fail(f"assertion observation {key!r} must be true")


def require_probe_status(observations: dict[str, object], key: str) -> None:
    value = observations.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or not 200 <= value < 400:
        fail(f"assertion observation {key!r} must be a successful HTTP status")


def validate_assertion_observations(name: str, observations: object) -> None:
    if not isinstance(observations, dict):
        fail(f"assertion {name!r} observations must be an object")
    required = REQUIRED_OBSERVATIONS.get(name)
    if required is None:
        if not observations:
            fail(f"additional assertion {name!r} observations must not be empty")
        return
    missing = sorted(required - observations.keys())
    if missing:
        fail(f"assertion {name!r} observations are missing: {', '.join(missing)}")
    for key in required:
        value = observations[key]
        if value is None or (isinstance(value, str) and not value.strip()):
            fail(f"assertion {name!r} observation {key!r} is empty")

    for key in (
        "empty_state_verified",
        "persistence_verified",
        "terminal_state_verified",
        "correlation_verified",
        "aggregation_verified",
        "certificate_verified",
        "checksums_verified",
        "source_absent",
        "destruction_verified",
        "content_verified",
    ):
        if key in observations:
            require_true(observations, key)
    for key in ("probe_status", "status_code", "application_probe_status"):
        if key in observations:
            require_probe_status(observations, key)
    for key in (
        "engines_tested",
        "engines_passed",
        "replacements_tested",
        "replacements_passed",
        "fault_points_total",
        "fault_points_passed",
        "operations_recovered",
        "duplicates_injected",
        "records_captured",
        "samples_captured",
        "deliveries_attempted",
        "deliveries_confirmed",
        "services_healthy",
        "services_restarted",
        "artifact_count",
        "restored_artifact_count",
        "files_scanned",
        "resources_created",
    ):
        if key in observations:
            require_positive_integer(observations, key)
    for key in ("duplicate_side_effects", "credential_findings", "residual_resources"):
        if key in observations:
            require_zero(observations, key)
    if "max_recovery_seconds" in observations:
        value = observations["max_recovery_seconds"]
        if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 0:
            fail("assertion observation 'max_recovery_seconds' must be a non-negative number")
    for tested, passed in (
        ("engines_tested", "engines_passed"),
        ("replacements_tested", "replacements_passed"),
        ("fault_points_total", "fault_points_passed"),
        ("deliveries_attempted", "deliveries_confirmed"),
    ):
        if tested in observations and observations[tested] != observations[passed]:
            fail(f"assertion observations {tested!r} and {passed!r} must match")
    if "image_digest" in observations and not re.fullmatch(r"sha256:[0-9a-f]{64}", str(observations["image_digest"])):
        fail("assertion observation 'image_digest' must be an immutable SHA-256 digest")
    if "provisioned_at" in observations:
        timestamp(observations["provisioned_at"], "fresh_install.provisioned_at")


def validate_assertion_artifact(
    path: pathlib.Path,
    name: str,
    expected_mode: str,
    expected_commit: str,
    expected_nonce: str,
    instance_id: str,
) -> None:
    try:
        proof = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        fail(f"assertion {name!r} artifact is not valid JSON: {exc}")
    if not isinstance(proof, dict) or proof.get("schema_version") != ASSERTION_SCHEMA:
        fail(f"assertion {name!r} artifact must use schema_version {ASSERTION_SCHEMA!r}")
    expected_fields = {
        "mode": expected_mode,
        "assertion": name,
        "commit_sha": expected_commit,
        "run_nonce": expected_nonce,
        "fresh_instance_id": instance_id,
    }
    for field, expected in expected_fields.items():
        if proof.get(field) != expected:
            fail(f"assertion {name!r} artifact field {field!r} does not match this run")
    evidence = proof.get("evidence")
    if not isinstance(evidence, list) or not evidence or any(
        not isinstance(item, str) or not item.strip() for item in evidence
    ):
        fail(f"assertion {name!r} artifact evidence must be a non-empty string array")
    validate_assertion_observations(name, proof.get("observations"))


def reject_reused_instance(history_root: pathlib.Path, mode: str, instance_id: str) -> None:
    if not history_root.exists():
        return
    for prior_path in history_root.glob("*/manifest.json"):
        try:
            prior = json.loads(prior_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if prior.get("mode") == mode and prior.get("fresh_instance_id") == instance_id:
            fail(f"fresh_instance_id was already used by {prior_path}")


def main() -> None:
    if len(sys.argv) != 8:
        fail(
            "usage: validate-parity-runner-evidence.py "
            "MANIFEST MODE COMMIT STARTED_AT RUN_NONCE EVIDENCE_ROOT HISTORY_ROOT"
        )
    manifest_path = pathlib.Path(sys.argv[1])
    expected_mode = sys.argv[2]
    expected_commit = sys.argv[3]
    invocation_started = timestamp(sys.argv[4], "invocation started_at")
    expected_nonce = sys.argv[5]
    evidence_root = pathlib.Path(sys.argv[6]).resolve()
    history_root = pathlib.Path(sys.argv[7]).resolve()
    required_assertions = REQUIRED_ASSERTIONS.get(expected_mode)
    if required_assertions is None:
        fail(f"mode {expected_mode!r} has no evidence policy")
    if not evidence_root.is_dir():
        fail("evidence root does not exist")
    if manifest_path.is_symlink() or not manifest_path.is_file():
        fail("manifest is missing, not regular, or a symlink")
    try:
        manifest_path.resolve().relative_to(evidence_root)
    except ValueError:
        fail("manifest path escapes evidence root")
    try:
        document = json.loads(manifest_path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        fail("runner did not create the requested manifest")
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"manifest is unreadable or invalid JSON: {exc}")
    if not isinstance(document, dict):
        fail("manifest root must be an object")
    if document.get("schema_version") != SCHEMA:
        fail(f"schema_version must be {SCHEMA!r}")
    if document.get("mode") != expected_mode:
        fail(f"mode must be {expected_mode!r}")
    if document.get("commit_sha") != expected_commit:
        fail("commit_sha does not match the checked-out source")
    if document.get("run_nonce") != expected_nonce:
        fail("run_nonce does not match this runner invocation")
    instance_id = document.get("fresh_instance_id")
    if not isinstance(instance_id, str) or not instance_id.strip():
        fail("fresh_instance_id must be non-empty")
    reject_reused_instance(history_root, expected_mode, instance_id)
    started = timestamp(document.get("started_at"), "started_at")
    completed = timestamp(document.get("completed_at"), "completed_at")
    now = dt.datetime.now(dt.timezone.utc)
    if started < invocation_started - dt.timedelta(seconds=2):
        fail("started_at predates this runner invocation")
    if completed < started:
        fail("completed_at predates started_at")
    if started > now + FUTURE_SKEW or completed > now + FUTURE_SKEW:
        fail("runner timestamps are implausibly in the future")
    assertions = document.get("assertions")
    if not isinstance(assertions, list) or not assertions:
        fail("assertions must be a non-empty array")
    assertion_names: set[str] = set()
    for index, assertion in enumerate(assertions):
        if (
            not isinstance(assertion, dict)
            or not isinstance(assertion.get("name"), str)
            or not assertion["name"].strip()
            or assertion.get("passed") is not True
            or not isinstance(assertion.get("artifact"), str)
            or not assertion["artifact"].strip()
        ):
            fail(f"assertions[{index}] must contain name, passed=true, and artifact")
        name = assertion["name"]
        if name in assertion_names:
            fail(f"assertion name is duplicated: {name!r}")
        assertion_names.add(name)
    missing_assertions = sorted(required_assertions - assertion_names)
    if missing_assertions:
        fail(f"required assertions are missing: {', '.join(missing_assertions)}")
    components = document.get("components")
    if not isinstance(components, dict) or not components:
        fail("components must be a non-empty version map")
    if any(not isinstance(key, str) or not key or not isinstance(value, str) or not value for key, value in components.items()):
        fail("every component name and version must be a non-empty string")
    if document.get("redaction_passed") is not True:
        fail("redaction_passed must be true")
    if document.get("cleanup_passed") is not True:
        fail("cleanup_passed must be true")
    artifacts = document.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        fail("artifacts must be a non-empty array")
    artifact_paths: set[str] = set()
    artifact_records: dict[str, tuple[pathlib.Path, dict[str, object]]] = {}
    for index, artifact in enumerate(artifacts):
        if not isinstance(artifact, dict):
            fail(f"artifacts[{index}] must be an object")
        relative = artifact.get("path")
        expected_hash = artifact.get("sha256")
        if not isinstance(relative, str) or not relative.strip():
            fail(f"artifacts[{index}].path must be a non-empty relative path")
        if not isinstance(expected_hash, str) or not SHA256.fullmatch(expected_hash):
            fail(f"artifacts[{index}].sha256 must be a lowercase SHA-256 digest")
        if relative in artifact_paths:
            fail(f"artifact path is duplicated: {relative!r}")
        path = resolve_artifact(evidence_root, relative)
        actual_hash, size = hash_and_scan_artifact(path)
        if actual_hash != expected_hash:
            fail(f"artifact hash mismatch: {relative!r}")
        if "size_bytes" in artifact:
            declared_size = artifact["size_bytes"]
            if isinstance(declared_size, bool) or not isinstance(declared_size, int) or declared_size != size:
                fail(f"artifact size mismatch: {relative!r}")
        artifact_paths.add(relative)
        artifact_records[relative] = (path, artifact)
    retained_paths = retained_file_set(evidence_root, manifest_path)
    if retained_paths != artifact_paths:
        undeclared = sorted(retained_paths - artifact_paths)
        missing = sorted(artifact_paths - retained_paths)
        fail(f"retained file declaration mismatch: undeclared={undeclared} missing={missing}")
    assertion_artifacts: set[str] = set()
    for assertion in assertions:
        relative = assertion["artifact"]
        if relative not in artifact_paths:
            fail(f"assertion {assertion['name']!r} references an undeclared artifact")
        if relative in assertion_artifacts:
            fail("each assertion must reference a distinct structured artifact")
        assertion_artifacts.add(relative)
        path, artifact = artifact_records[relative]
        if artifact.get("media_type") != "application/json":
            fail(f"assertion {assertion['name']!r} artifact media_type must be application/json")
        validate_assertion_artifact(
            path,
            assertion["name"],
            expected_mode,
            expected_commit,
            expected_nonce,
            instance_id,
        )
    for value in walk_strings(document):
        if SECRET_VALUE.search(value):
            fail("manifest contains credential-like material")
    print(
        f"parity runner evidence: valid mode={expected_mode} "
        f"instance={instance_id} assertions={len(assertions)}"
    )


if __name__ == "__main__":
    main()
