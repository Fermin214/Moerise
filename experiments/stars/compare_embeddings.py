"""Compare embedding models while fixing corpus, UMAP settings and visual metadata."""

import argparse
import hashlib
import json
import os
from itertools import combinations
from pathlib import Path

for variable in ("NUMBA_NUM_THREADS", "OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"):
    os.environ[variable] = "2"

import numpy as np
from sklearn.neighbors import NearestNeighbors
from threadpoolctl import threadpool_limits

from engine import align, export_html, metric_context, read_export, report, validate, write_json
from tuning import PRESETS, build_variant


def compare(bundle, notes, matrix, valid, model, label, identifier, progress=print):
    lookup = {notes[index]["name"]: matrix[i] for i, index in enumerate(valid)}
    supplied = {note["name"]: note for note in notes}
    for note in bundle["notes"]:
        if note["name"] not in lookup or supplied[note["name"]]["content"] != note["content"]:
            raise ValueError("两套向量必须覆盖相同笔记与正文")
    descriptors = bundle.setdefault("embedding_models", [{
        "id": "minilm384", "label": "MiniLM · 384", "dimensions": bundle["source"]["dimensions"],
        "model": bundle["source"].get("model", "MiniLM"),
    }])
    descriptors[:] = [item for item in descriptors if item["id"] != identifier]
    descriptors.append({"id": identifier, "label": label, "model": model, "dimensions": matrix.shape[1]})
    seed = bundle["source"]["seed"]
    for scale in bundle["scales"]:
        shared, count = scale["base_count"], scale["count"]
        data = np.asarray([lookup[bundle["notes"][i]["name"]] for i in scale["indices"]])
        labels = np.asarray(scale["labels"])
        contexts = {"base": metric_context(data[:shared], seed),
                    "full": metric_context(data, seed, required=range(shared, count))}
        target = {"umap_variants": {}, "neighbors": []}
        distances, neighbors = NearestNeighbors(n_neighbors=min(11, count), metric="cosine", algorithm="brute").fit(data).kneighbors(data)
        for i, (row, scores) in enumerate(zip(neighbors, distances)):
            target["neighbors"].append([
                {"index": scale["indices"][int(j)], "similarity": round(float(1 - score), 5)}
                for j, score in zip(row, scores) if i != j
            ][:7])
        overlaps = [
            len({peer["index"] for peer in left} & {peer["index"] for peer in right}) / max(1, len(left))
            for left, right in zip(scale["neighbors"], target["neighbors"])
        ]
        target["source_neighbor_overlap_at_7"] = round(float(np.mean(overlaps)), 5)
        for preset in PRESETS:
            progress(f"{label} / {count} 条 / {preset['label']}")
            reference = scale["umap_variants"][preset["id"]]
            frame = np.asarray(reference["stages"]["base"]["coordinates"])
            variant = build_variant(data, labels, shared, seed, preset["parameters"], frame, contexts)
            old = np.asarray(reference["stages"]["frozen"]["coordinates"])
            new = np.asarray(variant["stages"]["frozen"]["coordinates"])
            aligned = align(old, new, count)
            radius = max(1e-10, float(np.percentile(np.linalg.norm(old - old.mean(axis=0), axis=1), 90)))
            variant["model_change"] = {
                "aligned_displacement_median": round(float(np.median(np.linalg.norm(aligned - old, axis=1)) / radius), 5),
                "aligned_displacement_p95": round(float(np.percentile(np.linalg.norm(aligned - old, axis=1), 95) / radius), 5),
            }
            target["umap_variants"][preset["id"]] = variant
        scale.setdefault("embedding_variants", {})[identifier] = target
        neighbor_sets = {"minilm384": scale["neighbors"]}
        neighbor_sets.update({key: value["neighbors"] for key, value in scale["embedding_variants"].items()})
        scale["embedding_neighbor_comparisons"] = [
            {
                "left": left, "right": right,
                "overlap_at_7": round(float(np.mean([
                    len({p["index"] for p in a} & {p["index"] for p in b}) / max(1, len(a))
                    for a, b in zip(neighbor_sets[left], neighbor_sets[right])
                ])), 5),
            }
            for left, right in combinations(neighbor_sets, 2)
        ]
    bundle["embedding_comparison"] = {
        "corpus": "同一批笔记正文，按资源 name 对齐；同一嵌套样本、种子和新增笔记留出",
        "controls": "UMAP 参数、主题成员、星体亮度和尺寸、色彩、密度核、取景规则固定",
        "interpretation": "模型选择会改变原向量邻域；相同维数的模型也可直接对照",
    }
    return bundle


def main():
    parser = argparse.ArgumentParser(description="在群星页面中增加相同笔记的 embedding 对照")
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--input", required=True)
    parser.add_argument("--vectors-f32", required=True)
    parser.add_argument("--dimensions", type=int, required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--id", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    bundle = json.loads(Path(args.bundle).read_text())
    raw_notes, vectors, source = read_export(args.input, args.vectors_f32, args.dimensions)
    notes, matrix, valid, _ = validate(raw_notes, vectors, source)
    with threadpool_limits(limits=2):
        compare(bundle, notes, matrix, valid, args.model, args.label, args.id,
                progress=lambda message: print(message, flush=True))
    bundle["embedding_models"][-1]["vectors_sha256"] = hashlib.sha256(Path(args.vectors_f32).read_bytes()).hexdigest()
    output = Path(args.out); output.mkdir(parents=True, exist_ok=True)
    write_json(output / "bundle.json", bundle)
    (output / "comparison.md").write_text(report(bundle), encoding="utf-8")
    export_html(output, bundle)
    print(str(output / "bundle.json"), flush=True)


if __name__ == "__main__":
    main()
