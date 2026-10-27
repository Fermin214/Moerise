#!/usr/bin/env python3
"""Compare retrieval modes against a local set of real questions using API v1."""

import argparse
import json
import math
import statistics
import time
import urllib.error
import urllib.request
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=".local/config.json")
    parser.add_argument("--questions", required=True, help='JSON array of {"query": "...", "expected": ["memos/..."]}')
    parser.add_argument("--url", default="http://127.0.0.1:8787")
    parser.add_argument("--credential", default="reader", help="Name of a notes:read credential in the config")
    parser.add_argument("--output", default=".local/search-evaluation.json")
    args = parser.parse_args()
    config = json.loads(Path(args.config).read_text(encoding="utf-8"))
    credentials = [item for item in config["access_tokens"] if item["name"] == args.credential and "notes:read" in item["scopes"]]
    if len(credentials) != 1:
        parser.error("select one configured notes:read credential")
    questions = json.loads(Path(args.questions).read_text(encoding="utf-8"))
    if not isinstance(questions, list) or not questions:
        parser.error("questions must be a nonempty JSON array")
    for question in questions:
        if not isinstance(question, dict) or not isinstance(question.get("query"), str) or not question["query"].strip() or not isinstance(question.get("expected", []), list) or any(not isinstance(name, str) or not name.startswith("memos/") for name in question.get("expected", [])):
            parser.error("each question needs query text and an optional list of expected memo names")
        if "state" not in question:
            archived = question.get("include_archived", False)
            if not isinstance(archived, bool):
                parser.error("include_archived must be a boolean when state is omitted")
            question["state"] = "ALL" if archived else "NORMAL"
        if question["state"] not in ("NORMAL", "ARCHIVED", "ALL"):
            parser.error("question state must be NORMAL, ARCHIVED, or ALL")
        if not isinstance(question.get("tags", []), list) or any(not isinstance(tag, str) for tag in question.get("tags", [])):
            parser.error("question tags must be a list of strings")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(path, body=None):
        request = urllib.request.Request(args.url.rstrip("/") + path,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers={"Authorization": "Bearer " + credentials[0]["token"], "Content-Type": "application/json"})
        start = time.perf_counter()
        try:
            with opener.open(request, timeout=100) as response:
                value = json.load(response)
        except urllib.error.HTTPError as error:
            value = {"error": {"code": "HTTP_" + str(error.code)}}
            try:
                detail = json.load(error).get("error", {})
                value["error"]["code"] = detail.get("code", value["error"]["code"])
            except (ValueError, AttributeError):
                pass
        except (OSError, urllib.error.URLError):
            value = {"error": {"code": "CONNECTION_FAILED"}}
        except (ValueError, UnicodeError):
            value = {"error": {"code": "INVALID_RESPONSE"}}
        if not isinstance(value, dict):
            value = {"error": {"code": "INVALID_RESPONSE"}}
        return value, round((time.perf_counter() - start) * 1000, 2)

    index, _ = call("/api/ext/v1/search/index")
    rows = []
    for number, question in enumerate(questions, 1):
        for mode in ("keyword", "semantic", "hybrid"):
            value, latency = call("/api/ext/v1/search", {"query": question["query"], "mode": mode, "limit": 5,
                                                        "tags": question.get("tags", []), "state": question["state"]})
            names = [hit["note"]["name"] for hit in value.get("results", [])]
            expected = set(question.get("expected", []))
            rank = next((i for i, name in enumerate(names, 1) if name in expected), None)
            semantic = value.get("semantic", {})
            degraded = value.get("error") is None and (
                value.get("mode") != mode or (mode != "keyword" and not semantic.get("used", False)))
            rows.append({"question": number, "query": question["query"], "requested_mode": mode, "actual_mode": value.get("mode"),
                         "state": question["state"], "latency_ms": latency, "names": names,
                         "semantic": semantic, "degraded": degraded, "error": value.get("error"),
                         "hit_at_5": None if not expected else rank is not None, "reciprocal_rank": None if not expected else 0 if rank is None else 1 / rank})
    summary = {}
    for mode in ("keyword", "semantic", "hybrid"):
        selected = [row for row in rows if row["requested_mode"] == mode]
        latencies = sorted(row["latency_ms"] for row in selected)
        scored = [row for row in selected if row["hit_at_5"] is not None]
        summary[mode] = {"queries": len(selected), "errors": sum(row["error"] is not None for row in selected),
                         "fallbacks": sum(row["degraded"] for row in selected),
                         "median_ms": statistics.median(latencies), "p95_ms": latencies[math.ceil(len(latencies) * .95) - 1],
                         "scored_queries": len(scored), "hit_at_5": None if not scored else sum(row["hit_at_5"] for row in scored) / len(scored),
                         "mrr_at_5": None if not scored else statistics.mean(row["reciprocal_rank"] for row in scored)}
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    # Evaluation questions and note identifiers can be private.
    with output.open("x", encoding="utf-8") as stream:
        output.chmod(0o600)
        json.dump({"index": index, "summary": summary, "results": rows}, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    print("Saved:", output)
    return int(index.get("error") is not None or any(row["error"] is not None or row["degraded"] for row in rows))


if __name__ == "__main__":
    raise SystemExit(main())
