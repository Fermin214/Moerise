#!/usr/bin/env python3
"""Merge stage 4 live reports without calling a model or scoring answer quality."""

import argparse
import hashlib
import json
import math
import statistics
import sys
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path


CONTRACT_DATASETS = (
    "c17d4c2bcfbc4be239436b5fede84570a6a2a8bd5314b1f412b167ac2ce48fd5",
    "4f1144482bc645bd3b5a07001ee00a606d4f99e6b0ff571cc08a5fb00f7c5d4d",
)
TIMELINE_IDS = {
    "timeline-cache-four-years", "timeline-basil-practice",
    "timeline-backup-maturity", "timeline-retry-design",
    "timeline-backfill-date-filter", "timeline-conflict-not-progress",
    "timeline-duplicate-event-date", "timeline-inclusive-start-exclusive-end",
}
REQUEST_DEFAULTS = {
    "names": [], "question": "", "tags": [], "state": "",
    "from": "", "until": "", "timeline": False, "stream": False,
}
TOKEN_FIELDS = ("input_tokens", "output_tokens", "total_tokens", "cached_tokens", "cache_creation_tokens")
LLM_FIELDS = (
    "protocol", "model", "timeout_seconds", "max_concurrency",
    "max_retries", "max_output_tokens", "context_bytes",
)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def result_sha256(result):
    """Fingerprint the full original AIResult, including evidence content."""
    return sha256(json.dumps(result, ensure_ascii=False, sort_keys=True,
                             separators=(",", ":"), allow_nan=False).encode("utf-8"))


def reject_constant(value):
    raise ValueError("non-finite JSON number: " + value)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key: " + key)
        result[key] = value
    return result


def load_json(path):
    data = Path(path).read_bytes()
    return json.loads(data.decode("utf-8"), object_pairs_hook=unique_object,
                      parse_constant=reject_constant), sha256(data)


def basename(path):
    return str(path).replace("\\", "/").rsplit("/", 1)[-1]


def timestamp(text):
    require(isinstance(text, str) and text, "missing request timestamp")
    parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    require(parsed.tzinfo is not None, "timestamp must include a timezone")
    return parsed.astimezone(timezone.utc)


def request_value(value):
    require(isinstance(value, dict), "request must be an object")
    require(not (value.keys() - REQUEST_DEFAULTS.keys()), "unknown request fields")
    result = {**REQUEST_DEFAULTS, **value}
    for key in ("names", "tags"):
        require(isinstance(result[key], list) and
                all(isinstance(item, str) for item in result[key]), "invalid request." + key)
    for key in ("question", "state", "from", "until"):
        require(isinstance(result[key], str), "invalid request." + key)
    require(type(result["timeline"]) is bool and type(result["stream"]) is bool,
            "timeline and stream must be booleans")
    return result


def validate_case(row, fixed, dataset_hash, report_hash):
    case = row.get("case")
    require(isinstance(case, dict), "missing report.case")
    require(case.keys() == fixed.keys(), fixed["id"] + ": case fields changed")
    for key in fixed.keys() - {"request"}:
        require(case[key] == fixed[key], fixed["id"] + ": case." + key + " changed")
    original = request_value(case["request"])
    expected = request_value(fixed["request"])
    contract_rejected = (
        (report_hash, dataset_hash) == CONTRACT_DATASETS and
        case["id"] in TIMELINE_IDS and bool(original["names"])
    )
    comparable = dict(original)
    if contract_rejected:
        comparable["names"] = []
    require(comparable == expected, fixed["id"] + ": request differs from the fixed dataset")
    sent = request_value(row.get("request"))
    require(sent == {**original, "stream": True},
            fixed["id"] + ": actual request differs from report.case")
    return contract_rejected


def validate_sources(report, notes):
    sources = {}
    for source in report.get("sources", []):
        name = source.get("name")
        require(name in notes and name not in sources, "unknown or duplicate report source")
        note = notes[name]
        require(source.get("content") == note["content"] and source.get("state") == note["state"],
                name + ": source content or state differs from the frozen corpus")
        for key in ("create_time", "update_time"):
            require(timestamp(source.get(key)) == timestamp(note[key]), name + ": changed " + key)
        require(isinstance(source.get("source_hash"), str) and source["source_hash"],
                name + ": missing source hash")
        sources[name] = source
    require(sources.keys() == notes.keys(), "report source snapshot is incomplete")
    return sources


def compact_reference(ref, notes, sources, index):
    name = ref.get("name")
    require(name in sources and name in notes, "reference outside the frozen corpus")
    source = sources[name]
    require(ref.get("id") == "E" + str(index + 1), name + ": invalid reference ID")
    for key in ("source_hash", "create_time", "update_time", "url"):
        require(ref.get(key) == source.get(key), name + ": reference " + key + " changed")
    body = notes[name]["content"].encode("utf-8")
    start, end = ref.get("byte_start"), ref.get("byte_end")
    require(type(start) is int and type(end) is int and 0 <= start < end <= len(body),
            name + ": invalid evidence byte range")
    text = body[start:end].decode("utf-8")
    require(text == ref.get("content"), name + ": evidence fragment differs from dataset")
    first = 1 + body[:start].count(b"\n")
    last = first + text.removesuffix("\n").count("\n")
    require(ref.get("line_start") == first and ref.get("line_end") == last,
            name + ": invalid evidence line range")
    return {**{key: value for key, value in ref.items() if key != "content"},
            "dataset_id": notes[name]["id"], "fragment_sha256": sha256(body[start:end])}


def usage_value(value):
    require(value is None or isinstance(value, dict), "usage must be an object or null")
    result = {key: (value or {}).get(key) for key in TOKEN_FIELDS}
    for key, number in result.items():
        require(number is None or type(number) is int and number >= 0, "invalid usage." + key)
    return result


def cache_class(usage):
    cached = usage["cached_tokens"]
    return "unknown" if cached is None else "hit" if cached > 0 else "miss"


def compact_result(result, notes, sources):
    references = result.get("references", [])
    require(isinstance(references, list), "result.references must be a list")
    compact = [compact_reference(ref, notes, sources, i) for i, ref in enumerate(references)]
    ids = {ref["id"] for ref in compact}
    require(result.get("status") in ("answered", "insufficient"), "invalid completed result status")
    for statement in result.get("statements", []):
        require(set(statement.get("citations", [])) <= ids, "completed result cites unknown evidence")
    return {**{key: value for key, value in result.items() if key != "references"},
            "references": compact}


def attempt(row, report, report_index, location, fixed, notes, sources, dataset_hash):
    legacy_contract = validate_case(row, fixed, dataset_hash, report["dataset_sha256"])
    result = row.get("result")
    require(result is None or isinstance(result, dict), "AIResult must be an object or null")
    complete = isinstance(result, dict) and result.get("completed") is True
    eligible = (
        complete and row.get("hard_invariants_ok") is True and row.get("http_status") == 200 and
        not any(row.get(key) for key in ("error", "transport_error", "protocol_errors", "hard_failures"))
        and not legacy_contract
    )
    outcome = "contract_rejected" if legacy_contract else "complete" if eligible else "failed"
    usage = usage_value(result.get("usage") if result is not None else row.get("usage"))
    summary = {
        "attempt_id": report["file"] + "#" + location,
        "source_report": report["file"], "source_report_sha256": report["file_sha256"],
        "dataset_sha256": report["dataset_sha256"], "started_at": row.get("started_at"),
        "request_id": row.get("request_id"), "http_status": row.get("http_status"),
        "endpoint": row.get("endpoint"), "outcome": outcome,
        "result_completed": complete, "hard_invariants_ok": row.get("hard_invariants_ok") is True,
        "result_status": (result or {}).get("status"),
        "result_sha256": result_sha256(result) if result is not None else None,
        "error": row.get("error"), "transport_error": row.get("transport_error"),
        "protocol_errors": row.get("protocol_errors", []), "hard_failures": row.get("hard_failures", []),
        "partial_statement_count": len(row.get("content") or []),
        "reference_count": len(row.get("references") or []),
        "metrics": result.get("metrics") if result is not None else row.get("metrics"),
        "client_metrics": row.get("client_metrics"), "usage": usage, "cache": cache_class(usage),
        "process": row.get("process"), "llm": report["llm"],
        "response_model": (result or {}).get("model"),
    }
    if legacy_contract:
        summary["contract_request_names"] = row["case"]["request"]["names"]
    reduced = compact_result(result, notes, sources) if eligible else None
    # Report order breaks timestamp ties; timestamps remain visible in the output.
    order = (timestamp(row.get("started_at")), report_index,
             tuple(int(part) for part in location.split("/") if part.isdigit()))
    return {"summary": summary, "result": reduced, "order": order}


def distribution(values, total):
    numbers = sorted(value for value in values if value is not None)
    require(all(type(value) in (int, float) and math.isfinite(value) and value >= 0
                for value in numbers), "invalid nonnegative metric")
    count = len(numbers)
    return {
        "n": count, "missing": total - count,
        "median": statistics.median(numbers) if count else None,
        "p95": numbers[math.ceil(count * 0.95) - 1] if count else None,
    }


def aggregate(attempts):
    rows = [entry["summary"] for entry in attempts]
    result = {
        "requests": len(rows), "outcomes": dict(Counter(row["outcome"] for row in rows)),
        "cache": {key: sum(row["cache"] == key for row in rows) for key in ("hit", "miss", "unknown")},
        "latency_ms": {}, "usage": {},
    }
    for group, keys in (
        ("metrics", ("retrieval_ms", "first_content_ms", "generation_ms", "total_ms")),
        ("client_metrics", ("references_ms", "first_content_ms", "total_ms")),
    ):
        result["latency_ms"][group] = {
            key: distribution([(row.get(group) or {}).get(key) for row in rows], len(rows)) for key in keys
        }
    for key in TOKEN_FIELDS:
        values = [row["usage"][key] for row in rows]
        result["usage"][key] = {
            **distribution(values, len(rows)),
            "sum_reported": sum(value for value in values if value is not None)
            if any(value is not None for value in values) else None,
        }
    return result


def attach_reviews(paths, cases, attempts, inputs):
    for path in paths:
        data, digest = load_json(path)
        rows = data.get("reviews") if isinstance(data, dict) else data
        require(isinstance(rows, list), "reviews must be a list or an object with reviews")
        file = basename(path)
        inputs.append({"file": file, "sha256": digest, "reviews": len(rows)})
        for raw in rows:
            require(isinstance(raw, dict) and raw.get("id") in cases, file + ": unknown review case")
            require(isinstance(raw.get("verdict"), str) and raw["verdict"], file + ": missing review verdict")
            require(isinstance(raw.get("notes"), (str, list)), file + ": missing review notes")
            target = cases[raw["id"]]
            history = {
                **raw, "review_file": file, "review_file_sha256": digest,
                "reviewer": raw.get("reviewer", data.get("reviewer") if isinstance(data, dict) else None),
                "binding": "unbound", "attempt_id": None, "applied_to_selected": False,
            }
            source = raw.get("source_report")
            if source:
                require(isinstance(source, str), "review.source_report must be a file name")
                candidates = [
                    entry for entry in attempts[raw["id"]]
                    if entry["summary"]["source_report"] == basename(source) and entry["result"] is not None
                ]
                if raw.get("result_sha256"):
                    candidates = [entry for entry in candidates
                                  if entry["summary"]["result_sha256"] == raw["result_sha256"]]
                history["binding"] = "no_matching_complete_result" if not candidates else "ambiguous"
                if len(candidates) == 1:
                    identity = candidates[0]["summary"]["attempt_id"]
                    history["attempt_id"] = identity
                    selected = target["selected_attempt"]
                    history["binding"] = "selected_result" if selected == identity else "historical_result"
            target["agent_review_history"].append(history)
    for case in cases.values():
        applicable = [review for review in case["agent_review_history"] if review["binding"] == "selected_result"]
        if applicable:
            applicable[-1]["applied_to_selected"] = True
            case["agent_review"] = dict(applicable[-1])


def summarize(dataset_path, report_paths, review_paths):
    dataset, dataset_hash = load_json(dataset_path)
    require(dataset.get("version") == 1, "unsupported dataset schema version")
    fixed = {case["id"]: case for case in dataset["cases"]}
    notes = {"memos/" + note["id"]: note for note in dataset["notes"]}
    require(len(fixed) == len(dataset["cases"]) and len(notes) == len(dataset["notes"]),
            "duplicate dataset case or note ID")
    reports, by_case, batches = [], {key: [] for key in fixed}, []
    performance_by_level = {1: [], 2: [], 4: []}
    report_names, report_hashes = set(), set()
    for report_index, path in enumerate(report_paths):
        report, digest = load_json(path)
        file = basename(path)
        require(file not in report_names and digest not in report_hashes, "duplicate report file or SHA256")
        report_names.add(file)
        report_hashes.add(digest)
        report_hash = report.get("dataset_sha256")
        allowed = {dataset_hash}
        if dataset_hash == CONTRACT_DATASETS[1]:
            allowed.add(CONTRACT_DATASETS[0])
        require(report_hash in allowed, file + ": unknown dataset SHA256")
        require(report.get("version") == 1, file + ": unsupported report schema version")
        sources = validate_sources(report, notes)
        meta = {
            "file": file, "file_sha256": digest, "dataset_sha256": report_hash,
            "started_at": report.get("started_at"), "finished_at": report.get("finished_at"),
            "execution": report.get("execution"), "llm": {
                key: report.get("llm", {}).get(key) for key in LLM_FIELDS
            },
            "retrieval": report.get("retrieval"), "environment": report.get("environment"),
            "case_attempts": len(report.get("runs", [])),
        }
        reports.append(meta)

        def read_attempt(row, location):
            case_id = (row.get("case") or {}).get("id")
            require(case_id in fixed, file + ": unknown case ID")
            return attempt(row, meta, report_index, location, fixed[case_id], notes, sources, dataset_hash)

        for index, row in enumerate(report.get("runs", [])):
            entry = read_attempt(row, "runs/" + str(index))
            by_case[row["case"]["id"]].append(entry)
        for index, batch in enumerate(report.get("concurrency_runs", [])):
            level = batch.get("concurrency")
            require(type(level) is int and level in (1, 2, 4), file + ": invalid concurrency level")
            entries = []
            for number, row in enumerate(batch.get("requests", [])):
                require(row.get("case", {}).get("id") == batch.get("case_id"),
                        file + ": concurrency batch mixes cases")
                entry = read_attempt(row, "concurrency_runs/" + str(index) + "/requests/" + str(number))
                entries.append(entry)
            performance_by_level[level].extend(entries)
            batches.append({
                "source_report": file, "source_report_sha256": digest, "case_id": batch.get("case_id"),
                "concurrency": level, "wall_ms": batch.get("wall_ms"), "process": batch.get("process"),
                "requests": [entry["summary"] for entry in entries], "summary": aggregate(entries),
            })
    cases, selected_entries, all_attempts, mismatches, latest_failures = {}, [], [], [], []
    for case_id, frozen in fixed.items():
        entries = sorted(by_case[case_id], key=lambda entry: entry["order"])
        all_attempts.extend(entries)
        completed = [entry for entry in entries if entry["result"] is not None]
        selected = completed[-1] if completed else None
        latest = entries[-1] if entries else None
        if selected:
            selected_entries.append(selected)
        if latest and latest["summary"]["outcome"] != "complete":
            latest_failures.append({"id": case_id, "attempt_id": latest["summary"]["attempt_id"],
                                    "outcome": latest["summary"]["outcome"], "error": latest["summary"]["error"]})
        actual = selected["result"]["status"] if selected else None
        comparison = {"expected": frozen["expected"]["status"], "actual": actual,
                      "matches": actual == frozen["expected"]["status"] if selected else None}
        if selected and not comparison["matches"]:
            mismatches.append({"id": case_id, **comparison,
                               "attempt_id": selected["summary"]["attempt_id"]})
        cases[case_id] = {
            **frozen, "attempts": [entry["summary"] for entry in entries],
            "selected_attempt": selected["summary"]["attempt_id"] if selected else None,
            "selected_result_sha256": selected["summary"]["result_sha256"] if selected else None,
            "latest_attempt": latest["summary"]["attempt_id"] if latest else None,
            "latest_attempt_failed": latest is not None and latest["summary"]["outcome"] != "complete",
            "result": selected["result"] if selected else None, "status_comparison": comparison,
            "agent_review": None, "agent_review_history": [], "human_review": None,
        }
    review_inputs = []
    attach_reviews(review_paths, cases, by_case, review_inputs)
    missing = [case_id for case_id, case in cases.items() if case["result"] is None]
    return {
        "version": 1,
        "dataset": {
            "file": str(dataset_path), "sha256": dataset_hash, "schema_version": dataset["version"],
            "notes": [{**{key: value for key, value in note.items() if key != "content"},
                       "name": "memos/" + note["id"], "content_sha256": sha256(note["content"].encode("utf-8"))}
                      for note in dataset["notes"]],
        },
        "policies": {
            "selection": "Latest chronologically complete, HTTP 200, hard_invariants_ok result without errors; performance requests do not replace case results.",
            "complete": "All dataset cases have selected complete results and no later failed attempt. This is execution coverage, not semantic approval.",
            "legacy_contract": "Only the known old/new SHA pair permits the eight timeline request.names differences; old timeline attempts are contract_rejected and never selected.",
            "result_sha256": "SHA256 of the full original AIResult encoded as UTF-8 JSON with sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False.",
            "agent_review": "Only explicit source_report binding to the selected generation is applied; optional result_sha256 must match. Last matching review in CLI order wins; all reviews remain in history.",
            "human_review": "Always null; agent review is separate from human review.",
            "statistics": "Median uses statistics.median (the mean of the middle two values for even sample counts). P95 uses nearest rank: sorted[ceil(0.95*n)-1]. Each metric records n and missing; null usage is never zero-filled.",
            "reference_content": "Resolve dataset_id in dataset notes, encode content as UTF-8, slice [byte_start:byte_end], then decode UTF-8. Fragment SHA256 verifies the slice.",
        },
        "reports": reports, "review_files": review_inputs,
        "summary": {
            "complete": not missing and not latest_failures, "dataset_cases": len(fixed),
            "selected_complete": len(selected_entries), "missing_complete": missing,
            "attempts": len(all_attempts), "attempt_outcomes": dict(Counter(entry["summary"]["outcome"] for entry in all_attempts)),
            "latest_failed_attempts": latest_failures,
            "expected_status_counts": dict(Counter(case["expected"]["status"] for case in fixed.values())),
            "selected_status_counts": dict(Counter(entry["result"]["status"] for entry in selected_entries)),
            "status_mismatch_count": len(mismatches), "status_mismatches": mismatches,
            "agent_reviewed_selected": sum(case["agent_review"] is not None for case in cases.values()),
            "agent_review_verdicts": dict(Counter(case["agent_review"]["verdict"] for case in cases.values() if case["agent_review"])),
            "human_review": None,
        },
        "metrics": {
            "selected_results": aggregate(selected_entries), "all_case_attempts": aggregate(all_attempts),
            "case_attempts_by_outcome": {
                outcome: aggregate([entry for entry in all_attempts if entry["summary"]["outcome"] == outcome])
                for outcome in ("complete", "failed", "contract_rejected")
            },
        },
        "concurrency": {
            "batches": batches,
            "by_level": {str(level): aggregate(performance_by_level[level]) for level in (1, 2, 4)},
        },
        "cases": list(cases.values()),
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True, help="Frozen stage4-cases.json")
    parser.add_argument("--reports", nargs="+", action="extend", required=True, help="Live reports; may be repeated")
    parser.add_argument("--reviews", nargs="+", action="extend", default=[],
                        help="Review lists or objects with reviews; bind each row with source_report and optional result_sha256")
    parser.add_argument("--output", required=True, help="New compact JSON path; refuses to overwrite")
    args = parser.parse_args(argv)
    try:
        output = summarize(args.dataset, args.reports, args.reviews)
        encoded = json.dumps(output, ensure_ascii=False, indent=2, allow_nan=False) + "\n"
        path = Path(args.output)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("x", encoding="utf-8", newline="\n") as stream:
            path.chmod(0o600)
            stream.write(encoded)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print("summarize-ai: " + str(error), file=sys.stderr)
        return 1
    summary = output["summary"]
    print(json.dumps({"output": str(path), "complete": summary["complete"],
                      "selected_complete": summary["selected_complete"],
                      "dataset_cases": summary["dataset_cases"],
                      "status_mismatches": summary["status_mismatch_count"],
                      "latest_failed_attempts": len(summary["latest_failed_attempts"])}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
