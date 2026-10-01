#!/usr/bin/env python3
"""Fail closed on the pinned Part 12 catalog and Updater release evidence."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import subprocess
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
COMMIT = re.compile(r"[0-9a-f]{40}\Z")
SHA = re.compile(r"[0-9a-f]{64}\Z")
TAG = re.compile(r"updater-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
ROW = re.compile(r"^\| \*\*([A-Z]+-[0-9]+)\*\* \| (.+) \| (.+) \|\s*$")


def stop(message: str) -> None:
    raise SystemExit(message)


def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load(path: pathlib.Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def catalog_ids(policy: dict, docs: pathlib.Path) -> list[str]:
    revision = policy.get("catalog_revision", "")
    if not COMMIT.fullmatch(revision):
        stop("Central policy revision is not an immutable commit SHA")
    actual = subprocess.check_output(["git", "-C", str(docs), "rev-parse", "HEAD"], text=True).strip()
    if actual != revision:
        stop("Central policy checkout differs from the pinned revision")
    catalog = docs / policy.get("catalog_path", "")
    if not catalog.is_file() or sha(catalog) != policy.get("catalog_sha256"):
        stop("Part 12 catalog bytes differ from the pinned SHA-256")
    body = catalog.read_text(encoding="utf-8")
    seen: set[str] = set()
    for line in body.splitlines():
        if not re.match(r"^\| \*\*[A-Z]+-[0-9]+\*\* \|", line):
            continue
        match = ROW.fullmatch(line)
        if not match or match.group(1) in seen or not match.group(2).strip() or not match.group(3).strip():
            stop("Part 12 has a duplicate, malformed or empty problem row")
        seen.add(match.group(1))
    if not seen:
        stop("Part 12 has no active problem IDs")
    ids = sorted(seen)
    if hashlib.sha256(("\n".join(ids) + "\n").encode()).hexdigest() != policy.get("catalog_ids_sha256"):
        stop("Part 12 ID set changed without a new Updater applicability review")
    for target in re.findall(r"\]\(([^)]+)\)", body):
        if target.startswith(("https://", "http://", "mailto:", "#")):
            continue
        relative = target.split("#", 1)[0]
        if relative and not (catalog.parent / relative).resolve().is_file():
            stop(f"Broken local Part 12 link: {relative}")
    return ids


def classify(policy: dict, ids: list[str], final: bool, run_url: str) -> list[dict]:
    na: dict[str, str] = {}
    for name, group in policy.get("not_applicable", {}).items():
        reason = group.get("reason", "").strip()
        if len(reason) < 50:
            stop(f"{name}: N/A needs an inspected component and concrete reason")
        for problem_id in group.get("ids", []):
            if problem_id in na:
                stop(f"Duplicate N/A classification: {problem_id}")
            na[problem_id] = reason
    final_ids = policy.get("final_signed_ids", [])
    if len(final_ids) != len(set(final_ids)) or any(problem_id in na for problem_id in final_ids):
        stop("Final signed IDs are duplicated or classified as N/A")
    evidence_by_prefix = policy.get("evidence_by_prefix", {})
    checks: list[dict] = []
    for problem_id in ids:
        if problem_id in na:
            checks.append({"id": problem_id, "status": "N/A", "evidence": [], "reason": na[problem_id]})
            continue
        prefix = problem_id.split("-", 1)[0]
        evidence = evidence_by_prefix.get(prefix)
        if not isinstance(evidence, list) or not evidence:
            stop(f"No reproducible evidence mapping for {problem_id}")
        checks.append({
            "id": problem_id,
            "status": "PASS" if final or problem_id not in final_ids else "PENDING_FINAL",
            "evidence": evidence + ([run_url] if run_url else []),
            "reason": None,
        })
    if set(na) - set(ids) or set(final_ids) - set(ids):
        stop("Policy classifies an ID absent from the pinned catalog")
    return checks


def version(tag: str, revision: str, main_revision: str) -> str:
    if not TAG.fullmatch(tag) or tag == "updater-v0.0.0":
        stop("Updater release tag is not exact stable updater-vMAJOR.MINOR.PATCH")
    if not COMMIT.fullmatch(revision) or revision != main_revision:
        stop("Updater release tag does not point at current main")
    return tag.removeprefix("updater-v")


def archive_parts(archive: pathlib.Path) -> tuple[bytes, bytes]:
    with tarfile.open(archive, "r:gz") as tar:
        installer = tar.extractfile("updater/install.sh")
        unit = tar.extractfile("updater/systemd/updater.service")
        if installer is None or unit is None:
            stop("Signed Updater archive has no installer or unit")
        return installer.read(), unit.read()


def candidate_values(artifacts: pathlib.Path, release_version: str) -> dict[str, str]:
    archive = artifacts / f"updater-{release_version}-install.tar.gz"
    installer, unit = archive_parts(archive)
    return {
        "binary_sha256": sha(artifacts / "updater-linux-amd64"),
        "installer_script_sha256": hashlib.sha256(installer).hexdigest(),
        "unit_sha256": hashlib.sha256(unit).hexdigest(),
    }


def verify_signed(artifacts: pathlib.Path, release_version: str, candidate: dict) -> None:
    manifest = artifacts / "updater-release.json"
    envelope = load(artifacts / "updater-release.json.sig.json")
    data = load(manifest)
    repository = os.getenv("GITHUB_REPOSITORY", "psewdon1m-exocortex/updater")
    base = f"https://github.com/{repository}/releases/download/updater-v{release_version}"
    archive_name = f"updater-{release_version}-install.tar.gz"
    if data != {
        "schema_version": 1,
        "service": "updater",
        "version": release_version,
        "binary": {"url": f"{base}/updater-linux-amd64", "sha256": sha(artifacts / "updater-linux-amd64")},
        "installer": {"url": f"{base}/{archive_name}", "sha256": sha(artifacts / archive_name)},
    }:
        stop("Signed Updater manifest differs from exact release assets")
    if candidate_values(artifacts, release_version) != candidate:
        stop("Signed Updater binary, installer or unit differs from pre-signing candidate")
    for name in ("updater-linux-amd64", archive_name, f"updater_{release_version}_amd64.deb"):
        checksum = artifacts / f"{name}.sha256"
        if checksum.read_text(encoding="utf-8").strip() != f"{sha(artifacts / name)}  {name}":
            stop(f"Updater checksum does not match {name}")
    if subprocess.check_output([str(artifacts / "updater-linux-amd64"), "version"], text=True).strip() != release_version:
        stop("Built Updater runtime version differs from release tag")
    public = artifacts / "updater.pem"
    der = subprocess.check_output(["openssl", "pkey", "-pubin", "-in", str(public), "-outform", "DER"])
    if envelope.get("schema") != "exocortex.release-signature.v1" or envelope.get("algorithm") != "RSA-PSS-SHA256" or envelope.get("key_id") != hashlib.sha256(der).hexdigest():
        stop("Updater release signature envelope or key ID is invalid")
    signature = base64.b64decode(envelope.get("signature", ""), validate=True)
    with tempfile.NamedTemporaryFile() as temporary:
        temporary.write(signature)
        temporary.flush()
        result = subprocess.run(["openssl", "dgst", "-sha256", "-verify", str(public), "-signature", temporary.name, "-sigopt", "rsa_padding_mode:pss", "-sigopt", "rsa_pss_saltlen:32", str(manifest)], capture_output=True)
        if result.returncode:
            stop("Updater release manifest signature failed verification")
    bootstrap = (artifacts / "bootstrap.sh").read_text(encoding="utf-8")
    if f'version="{release_version}"' not in bootstrap or base64.b64encode(public.read_bytes()).decode() not in bootstrap or "__UPDATER_BOOTSTRAP_" in bootstrap or "PRIVATE KEY" in bootstrap:
        stop("Updater bootstrap does not embed the exact signer and version")
    for path in artifacts.iterdir():
        if path.is_file() and b"-----BEGIN PRIVATE KEY-----" in path.read_bytes():
            stop(f"Private signer leaked into artifact {path.name}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("phase", choices=("lint", "pre", "verify", "final"))
    parser.add_argument("--docs", type=pathlib.Path, required=True)
    parser.add_argument("--policy", type=pathlib.Path, default=ROOT / ".github/known-problems-policy.json")
    parser.add_argument("--tag", default=os.getenv("GITHUB_REF_NAME", ""))
    parser.add_argument("--revision", default=os.getenv("GITHUB_SHA", ""))
    parser.add_argument("--main-revision", default="")
    parser.add_argument("--artifacts", type=pathlib.Path)
    parser.add_argument("--candidate", type=pathlib.Path)
    parser.add_argument("--pre-report", type=pathlib.Path)
    parser.add_argument("--public", type=pathlib.Path)
    parser.add_argument("--report", type=pathlib.Path)
    args = parser.parse_args()
    policy = load(args.policy)
    if policy.get("schema") != "exocortex.updater.known-problems-policy.v1":
        stop("Unsupported Updater known-problems policy")
    ids = catalog_ids(policy, args.docs)
    run_url = f"https://github.com/{os.getenv('GITHUB_REPOSITORY', 'psewdon1m-exocortex/updater')}/actions/runs/{os.getenv('GITHUB_RUN_ID')}" if os.getenv("GITHUB_RUN_ID") else ""
    classify(policy, ids, False, run_url)
    if args.phase == "lint":
        print(f"Pinned Part 12 catalog lint passed: {len(ids)} active IDs classified")
        return
    release_version = version(args.tag, args.revision, args.main_revision or args.revision)
    if args.phase == "pre":
        if not args.artifacts or not args.candidate or not args.report:
            stop("Pre-signing gate needs candidate artifacts and output paths")
        candidate = candidate_values(args.artifacts, release_version)
        args.candidate.write_text(json.dumps(candidate, indent=2) + "\n", encoding="utf-8")
        report = {
            "schema_version": 1,
            "service": "updater",
            "revision": args.revision,
            "release_tag": args.tag,
            "catalog_repository": policy["catalog_repository"],
            "catalog_revision": policy["catalog_revision"],
            "catalog_path": policy["catalog_path"],
            "catalog_sha256": policy["catalog_sha256"],
            "checks": classify(policy, ids, False, run_url),
        }
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"Updater pre-signing Part 12 gate passed for {args.tag}")
        return
    if not args.artifacts or not args.candidate or not args.pre_report:
        stop("Signed gate needs candidate metadata and pre-signing report")
    previous = load(args.pre_report)
    if previous.get("service") != "updater" or previous.get("revision") != args.revision or previous.get("release_tag") != args.tag or previous.get("catalog_sha256") != policy["catalog_sha256"] or previous.get("checks") != classify(policy, ids, False, run_url):
        stop("Updater pre-signing report is stale or incomplete")
    verify_signed(args.artifacts, release_version, load(args.candidate))
    if args.phase == "verify":
        print("Updater signed release matches the candidate and exact release manifest")
        return
    if not args.public or not args.report:
        stop("Final gate needs anonymous release downloads")
    local = sorted(path for path in args.artifacts.iterdir() if path.is_file())
    remote = sorted(path for path in args.public.iterdir() if path.is_file())
    if {path.name for path in local} != {path.name for path in remote}:
        stop("Published Updater release has missing or unexpected assets")
    for path in local:
        if sha(path) != sha(args.public / path.name):
            stop(f"Published Updater release asset differs: {path.name}")
    report = dict(previous)
    report["checks"] = classify(policy, ids, True, run_url)
    report["published_asset_sha256"] = {path.name: sha(path) for path in local}
    args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"Updater final Part 12 gate passed: {len(ids)} IDs and anonymous assets verified")


if __name__ == "__main__":
    main()
