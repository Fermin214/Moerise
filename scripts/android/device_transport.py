import argparse
import json
from pathlib import Path
import re

TRANSPORT = re.compile(r"Device server died during |\(host:transport:[^\n]*(?:: device offline| not found)")
EVIDENCE_ERRORS = (OSError, ValueError, TypeError, KeyError, AttributeError)


def command_failures(directory):
    reports = sorted(directory.rglob("commands.json"))
    if not reports:
        raise ValueError("commands.json missing")
    failures = []
    for report in reports:
        commands = json.loads(report.read_text(encoding="utf-8"))
        if not isinstance(commands, list) or not commands:
            raise ValueError("commands.json incomplete")
        for index, command in enumerate(commands):
            metadata = command.get("metadata")
            if not isinstance(metadata, dict) or not isinstance(metadata.get("status"), str):
                raise ValueError("command status missing")
            if metadata["status"] not in ("FAILED", "ERROR"):
                continue
            evaluated = metadata.get("evaluatedCommand") or command.get("command")
            if not isinstance(evaluated, dict) or len(evaluated) != 1:
                raise ValueError("failed command missing")
            kind = next(iter(evaluated))
            message = metadata.get("error", {}).get("message")
            sequence = metadata.get("sequenceNumber", index)
            if not isinstance(message, str) or not message or not isinstance(sequence, int):
                raise ValueError("failed command evidence incomplete")
            failures.append((sequence, kind, bool(TRANSPORT.search(message))))
    return sorted(failures, key=lambda failure: failure[0])


def classify_failure(root, member):
    groups = json.loads(Path(__file__).with_name("maestro").joinpath("catalog.json").read_text(encoding="utf-8"))["groups"]
    if member not in groups:
        rows = (root / "device-results.tsv").read_text(encoding="utf-8").splitlines()[1:]
        return ("assertion_failure" if any(row.split("\t")[:2] == [member, "failed"] for row in rows)
                else "evidence_incomplete"), None, False
    group = root / "maestro" / member
    results = json.loads((group / "results.json").read_text(encoding="utf-8"))
    if not isinstance(results, dict):
        return "evidence_incomplete", None, False
    cleanup_failed = any(isinstance(result, dict) and result.get("cleanup_errors") for result in results.values())
    if (not results or list(results) != groups[member][:len(results)]
            or any(not isinstance(result, dict) or "status" not in result for result in results.values())):
        return ("cleanup_failure" if cleanup_failed else "evidence_incomplete"), None, False
    first, screenshot_secondary = None, False
    classification = "transport_retryable"
    evidence_incomplete = False
    for flow, result in results.items():
        if result["status"] == "passed":
            continue
        try:
            failures = command_failures(group / flow / "artifacts")
        except EVIDENCE_ERRORS:
            evidence_incomplete = True
            continue
        if not failures:
            if result["status"] == "failed":
                classification = "assertion_failure"
            else:
                evidence_incomplete = True
            continue
        sequence, kind, transport = failures[0]
        if first is None:
            first = {"flow": flow, "command": kind, "sequence": sequence}
        screenshot_secondary |= any("screenshot" in later_kind.lower() and offline
                                    for _, later_kind, offline in failures[1:])
        if not transport or any(not offline for _, _, offline in failures):
            classification = "assertion_failure"
        elif ("screenshot" in kind.lower()
                or (result["status"], result.get("cause")) != ("environment_blocked", "maestro_driver_died")):
            evidence_incomplete = True
    if cleanup_failed:
        classification = "cleanup_failure"
    elif evidence_incomplete or (not first and classification == "transport_retryable"):
        classification = "evidence_incomplete"
    return classification, first, screenshot_secondary


def attempt_summary(root, suite, member, attempt, passed, adb_probe_ok, fixture_restored, retry_transport=True):
    passed = passed and adb_probe_ok and fixture_restored
    record = {"suite": suite, "member": member, "attempt": attempt, "outcome": "passed" if passed else "failed",
              "adb_probe_ok": adb_probe_ok, "fixture_restored": fixture_restored}
    if not passed:
        try:
            classification, first, screenshot_secondary = classify_failure(root, member)
        except EVIDENCE_ERRORS:
            classification, first, screenshot_secondary = "evidence_incomplete", None, False
        fixture_restored = fixture_restored and classification != "cleanup_failure"
        record.update(fixture_restored=fixture_restored, first_failure_command=first,
                      screenshot_secondary_failure=screenshot_secondary,
                      classification=classification if fixture_restored else "cleanup_failure",
                      retry_allowed=retry_transport and adb_probe_ok and fixture_restored
                      and classification == "transport_retryable" and attempt == 1)
    return record


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    parser.add_argument("suite")
    parser.add_argument("member")
    parser.add_argument("attempt", type=int, choices=(1, 2))
    parser.add_argument("--passed", action="store_true")
    parser.add_argument("--adb-probe-ok", action="store_true")
    parser.add_argument("--fixture-restored", action="store_true")
    parser.add_argument("--retry-transport", action="store_true")
    args = parser.parse_args()
    record = attempt_summary(args.root, args.suite, args.member, args.attempt, args.passed,
                             args.adb_probe_ok, args.fixture_restored, args.retry_transport)
    output = args.root / "device-transport" / args.suite / f"{args.member}-attempt-{args.attempt}.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    print("passed" if record["outcome"] == "passed" else "retry" if record["retry_allowed"] else "failed")


if __name__ == "__main__":
    main()
