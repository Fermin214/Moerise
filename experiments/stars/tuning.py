"""UMAP presets sharing notes, semantic metadata and rendering."""

import argparse
import copy
import os
import time
from pathlib import Path

for variable in ("NUMBA_NUM_THREADS", "OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"):
    os.environ[variable] = "2"

import numpy as np
from threadpoolctl import threadpool_limits


PRESETS = [
    {"id": "balanced", "label": "均衡", "description": "保留自然形状，作为对照",
     "parameters": {"n_neighbors": 15, "min_dist": .12, "spread": 1.0}},
    {"id": "clustered", "label": "轻聚落", "description": "略收紧近邻，增强局部分区",
     "parameters": {"n_neighbors": 12, "min_dist": .055, "spread": 1.05}},
    {"id": "connected", "label": "连贯聚落", "description": "扩大近邻范围，同时收紧局部",
     "parameters": {"n_neighbors": 24, "min_dist": .065, "spread": 1.1}},
]


def build_variant(data, labels, shared, seed, parameters, frame, contexts):
    from engine import align, incremental, layout, measure, rounded

    count = len(data)
    started = time.perf_counter()
    model, before, fitted = layout("umap", data[:shared], seed, parameters)
    base_seconds = time.perf_counter() - started
    started = time.perf_counter()
    _, after, _ = layout("umap", data, seed, parameters)
    after = align(before, after, shared)
    refit_seconds = time.perf_counter() - started
    started = time.perf_counter()
    frozen, mechanism = incremental(model, fitted, data, shared, before)
    frozen_seconds = time.perf_counter() - started
    # A single similarity transform preserves each layout's local structure.
    joined = align(frame, np.vstack([before, after, frozen]), shared)
    before, after, frozen = np.split(joined, [shared, shared + count])
    center = before.mean(axis=0)
    radius = max(1e-10, float(np.percentile(np.linalg.norm(before - center, axis=1), 90)))
    before, after, frozen = [(value - center) / radius for value in (before, after, frozen)]
    reference = np.asarray(rounded(before))
    stages = {}
    for stage, positions, seconds in [
        ("base", before, base_seconds), ("refit", after, refit_seconds),
        ("frozen", frozen, frozen_seconds),
    ]:
        coordinates = rounded(positions)
        stages[stage] = {
            "coordinates": coordinates, "seconds": round(seconds, 3),
            "metrics": measure(np.asarray(coordinates), contexts["base" if stage == "base" else "full"],
                               labels[:shared] if stage == "base" else labels, shared, reference),
        }
    return {"parameters": fitted, "incremental": mechanism, "stages": stages}


def tune_bundle(bundle, matrix, valid, progress=print):
    from engine import metric_context

    by_index = {index: matrix[i] for i, index in enumerate(valid)}
    seed = bundle["source"]["seed"]
    for scale in bundle["scales"]:
        count, shared = scale["count"], scale["base_count"]
        data = np.asarray([by_index[i] for i in scale["indices"]])
        labels = np.asarray(scale["labels"])
        contexts = {"base": metric_context(data[:shared], seed),
                    "full": metric_context(data, seed, required=range(shared, count))}
        scale["umap_variants"] = {"balanced": copy.deepcopy(scale["layouts"]["umap"])}
        scale["umap_variants"]["balanced"]["parameters"]["spread"] = 1.0
        for preset in PRESETS[1:]:
            progress(f"{count} 条：UMAP {preset['label']}")
            frame = np.asarray(scale["layouts"]["umap"]["stages"]["base"]["coordinates"])
            scale["umap_variants"][preset["id"]] = build_variant(
                data, labels, shared, seed, preset["parameters"], frame, contexts)
    bundle["umap_presets"] = PRESETS
    bundle["render_version"] = 3
    return bundle


def main():
    import json
    from engine import export_html, read_export, report, validate, write_json

    parser = argparse.ArgumentParser(description="为已有群星布局增加 UMAP 精调版本")
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--input", required=True)
    parser.add_argument("--vectors-f32")
    parser.add_argument("--dimensions", type=int)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    output = Path(args.out)
    output.mkdir(parents=True, exist_ok=True)
    bundle = json.loads(Path(args.bundle).read_text())
    notes, vectors, source = read_export(args.input, args.vectors_f32, args.dimensions)
    clean, matrix, valid, _ = validate(notes, vectors, source)
    if [note["name"] for note in clean] != [note["name"] for note in bundle["notes"]]:
        raise ValueError("向量文件的笔记顺序与布局文件不一致")
    if source["fingerprint"] != bundle["source"]["fingerprint"]:
        raise ValueError("向量或笔记内容与布局文件不一致")
    with threadpool_limits(limits=2):
        tune_bundle(bundle, matrix, valid, progress=lambda message: print(message, flush=True))
    write_json(output / "bundle.json", bundle)
    (output / "comparison.md").write_text(report(bundle), encoding="utf-8")
    export_html(output, bundle)
    print(str(output / "bundle.json"), flush=True)


if __name__ == "__main__":
    main()
