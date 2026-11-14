"""Independent semantic layout experiment. Source databases are opened read-only."""

import hashlib
import importlib.metadata
import json
import math
import re
import sqlite3
import time
from pathlib import Path
from urllib.parse import quote

import numpy as np
from scipy.linalg import orthogonal_procrustes
from scipy.spatial.distance import cdist
from sklearn.cluster import HDBSCAN
from sklearn.decomposition import PCA
from sklearn.metrics import silhouette_score
from sklearn.neighbors import NearestNeighbors
from threadpoolctl import threadpool_limits


METHODS = ("pca", "umap", "pacmap")


def write_json(path, value):
    path = Path(path)
    temporary = path.with_suffix(".partial")
    temporary.write_text(json.dumps(value, ensure_ascii=False, allow_nan=False), encoding="utf-8")
    temporary.replace(path)


def unit(vector):
    vector = np.asarray(vector, dtype=np.float64)
    norm = np.linalg.norm(vector)
    if vector.ndim != 1 or not np.isfinite(vector).all() or norm < 1e-12:
        raise ValueError("向量必须是一维、有限且非零的数值数组")
    return vector / norm


def read_index(path, instance=None, user=None, space=None):
    uri = "file:" + quote(str(Path(path).resolve()), safe="/") + "?mode=ro"
    connection = sqlite3.connect(uri, uri=True)
    connection.execute("PRAGMA query_only=ON")
    connection.execute("BEGIN")
    try:
        generations = connection.execute(
            """SELECT g.id, g.instance_id, g.user_name, g.space_hash, g.model,
                      g.dimensions, g.projection_revision
               FROM vector_generations g JOIN projection_meta p USING (instance_id)
               WHERE g.status = 'active' AND g.projection_revision = p.revision
               ORDER BY g.instance_id, g.user_name, g.space_hash"""
        ).fetchall()
        generations = [
            row for row in generations
            if (instance is None or row[1] == instance)
            and (user is None or row[2] == user)
            and (space is None or row[3] == space)
        ]
        if len(generations) != 1:
            raise ValueError("需要唯一的已发布向量代次；多个空间请使用 --instance、--user、--space 选择")
        generation, instance, user, space, model, dimensions, revision = generations[0]
        rows = connection.execute(
            """SELECT n.name, n.source_hash, m.payload_json, c.content_hash, c.vector
               FROM vector_notes n
               JOIN memos m ON m.name = n.name AND m.instance_id = ? AND m.user_name = ?
               LEFT JOIN vector_chunks c USING (generation_id, name)
               WHERE n.generation_id = ? AND json_extract(m.payload_json, '$.creator') = ?
               ORDER BY n.name, c.ordinal""",
            (instance, user, generation, user),
        )
        notes, vectors, seen, accumulated = [], [], set(), []
        name = None

        def finish():
            if name is not None:
                total = np.sum(accumulated, axis=0) if accumulated else None
                vectors.append(unit(total) if total is not None and np.linalg.norm(total) >= 1e-12 else None)

        for resource, source_hash, payload, chunk_hash, blob in rows:
            if resource != name:
                finish()
                name, seen, accumulated = resource, set(), []
                note = json.loads(payload)
                notes.append({
                    "name": resource, "content": note.get("content", ""),
                    "tags": note.get("tags") or [], "source_hash": source_hash,
                    "create_time": note.get("createTime", note.get("create_time", "")),
                    "state": note.get("state", "NORMAL"),
                })
            if chunk_hash is None or chunk_hash in seen:
                continue
            seen.add(chunk_hash)
            if blob is None or len(blob) != 4 * dimensions:
                raise ValueError(f"{resource} 的分块向量长度无效")
            accumulated.append(unit(np.frombuffer(blob, dtype="<f4")))
        finish()
        return notes, vectors, {
            "kind": "personal", "label": "Moerise 笔记索引", "model": model,
            "dimensions": dimensions, "generation": generation,
            "projection_revision": revision, "space_hash": space,
            "aggregation": "分块按 content_hash 去重，各块 L2 归一化，等权相加，再 L2 归一化",
            "content": "导出时的索引正文",
        }
    finally:
        connection.rollback()
        connection.close()


def read_export(path, vectors_path=None, dimensions=None, label=None, kind=None):
    value = json.loads(Path(path).read_text(encoding="utf-8"))
    if isinstance(value, list):
        notes, source = value, {}
    else:
        notes, source = value["notes"], value.get("source", {})
    if not isinstance(notes, list) or not notes:
        raise ValueError("导出文件需要非空 notes 数组")
    if vectors_path:
        if not dimensions or dimensions < 1:
            raise ValueError("二进制向量需要 --dimensions")
        raw = np.fromfile(vectors_path, dtype="<f4")
        if raw.size != len(notes) * dimensions:
            raise ValueError("二进制向量数量与笔记数量不匹配")
        raw = raw.reshape(len(notes), dimensions)
    else:
        raw = [note.get("vector") for note in notes]
    vectors = [None if vector is None else unit(vector) for vector in raw]
    source = {**source, "kind": kind or source.get("kind", "personal"),
              "label": label or source.get("label", "笔记向量导出"),
              "content": "导出文件中的笔记正文"}
    return notes, vectors, source


def validate(notes, vectors, source):
    clean, valid, names, dimensions = [], [], set(), None
    for note, vector in zip(notes, vectors, strict=True):
        name = note.get("name")
        if not isinstance(name, str) or not name or name in names:
            raise ValueError("每条笔记需要唯一且非空的 name")
        names.add(name)
        if not isinstance(note.get("content", ""), str):
            raise ValueError(f"{name} 的 content 必须是字符串")
        tags = note.get("tags") or []
        if not isinstance(tags, list) or not all(isinstance(tag, str) for tag in tags):
            raise ValueError(f"{name} 的 tags 必须是字符串数组")
        if vector is not None:
            dimensions = dimensions or len(vector)
            if not 1 <= dimensions <= 16384 or len(vector) != dimensions:
                raise ValueError("全部笔记向量必须来自同一维度的模型空间")
            valid.append(len(clean))
        clean.append({
            "name": name, "content": note.get("content", ""), "tags": tags,
            "title": title(note), "create_time": str(note.get("create_time", "")),
            "state": str(note.get("state", "NORMAL")),
        })
    if not valid:
        raise ValueError("没有可用于布局的笔记向量")
    matrix = np.asarray([vectors[i] for i in valid], dtype=np.float64)
    source["dimensions"] = dimensions
    source["total_notes"], source["vector_notes"] = len(clean), len(valid)
    source["excluded"] = len(clean) - len(valid)
    fingerprint = hashlib.sha256()
    for i in valid:
        fingerprint.update(json.dumps(clean[i], sort_keys=True, ensure_ascii=False).encode())
        fingerprint.update(np.asarray(vectors[i], dtype="<f8").tobytes())
    source["fingerprint"] = fingerprint.hexdigest()
    return clean, matrix, valid, source


def title(note):
    content = note.get("content", "").strip()
    first = next((line.strip(" #*>-`") for line in content.splitlines() if line.strip()), "")
    return first[:64] or note["name"]


def components(matrix, limit):
    count = min(limit, *matrix.shape)
    model = PCA(n_components=count, svd_solver="full")
    result = model.fit_transform(matrix)
    if count < limit:
        result = np.pad(result, ((0, 0), (0, limit - count)))
    return model, result


def align(reference, candidate, shared):
    """Similarity alignment removes translation, scale, rotation and reflection."""
    before = reference[:shared] - reference[:shared].mean(axis=0)
    after = candidate[:shared] - candidate[:shared].mean(axis=0)
    if np.linalg.norm(before) < 1e-10 or np.linalg.norm(after) < 1e-10:
        return candidate - candidate[:shared].mean(axis=0) + reference[:shared].mean(axis=0)
    rotation, _ = orthogonal_procrustes(after, before)
    scale = np.sum((after @ rotation) * before) / np.sum(after * after)
    return (candidate - candidate[:shared].mean(axis=0)) @ rotation * scale + reference[:shared].mean(axis=0)


def layout(method, matrix, seed, umap_parameters=None):
    count = len(matrix)
    if count < 12 or np.max(np.std(matrix, axis=0)) < 1e-10:
        model, coordinates = components(matrix, 2)
        return model, coordinates, {"implementation": "pca", "reason": "少于 12 条或向量完全相同，使用 PCA"}
    if method == "pca":
        model, coordinates = components(matrix, 2)
        return model, coordinates, {"implementation": "pca", "explained_variance": float(np.nan_to_num(model.explained_variance_ratio_).sum())}
    if method == "umap":
        import umap

        parameters = {"n_neighbors": min(15, count - 1), "min_dist": 0.12,
                      "spread": 1.0,
                      "metric": "cosine", "n_components": 2, "random_state": seed,
                      "transform_seed": seed, "n_jobs": 1, "init": "spectral"}
        parameters.update(umap_parameters or {})
        parameters["n_neighbors"] = min(parameters["n_neighbors"], count - 1)
        model = umap.UMAP(**parameters)
        return model, model.fit_transform(matrix.astype(np.float32)).astype(float), {"implementation": "umap", **parameters}
    import pacmap

    parameters = {"n_components": 2, "n_neighbors": min(10, max(2, (count - 1) // 4)),
                  "MN_ratio": 0.5, "FP_ratio": 2.0, "distance": "euclidean",
                  "apply_pca": False, "random_state": seed}
    model = pacmap.PaCMAP(**parameters)
    coordinates = model.fit_transform(matrix.astype(np.float32), init="pca")
    return model, coordinates.astype(float), {"implementation": "pacmap", **parameters, "init": "pca"}


def incremental(model, parameters, matrix, shared, before):
    new = matrix[shared:]
    if not len(new):
        return before.copy(), "无新增笔记"
    method = parameters["implementation"]
    if method == "pca":
        projected = model.transform(new)
        if projected.shape[1] < 2:
            projected = np.pad(projected, ((0, 0), (0, 2 - projected.shape[1])))
        mechanism = "固定 PCA 基底"
    elif method == "umap":
        projected = model.transform(new.astype(np.float32))
        mechanism = "UMAP.transform，固定旧点"
    else:
        projected = model.transform(new.astype(np.float32), basis=matrix[:shared].astype(np.float32))
        mechanism = "PaCMAP.transform，固定旧点"
    return np.vstack([before, projected]), mechanism


def clusters(matrix, notes, indices):
    count, dimensions = matrix.shape
    if count < 8:
        return np.full(count, -1, dtype=int), [], {"space": "归一化原向量", "reason": "少于 8 条，保留为独立笔记"}
    working = matrix
    space = "归一化原向量（欧氏距离）"
    if dimensions > 50 and count > 50:
        model, working = components(matrix, 50)
        space = f"归一化原向量 → PCA 50 维（保留方差 {np.nan_to_num(model.explained_variance_ratio_).sum():.1%}）"
    minimum = max(4, min(30, round(math.sqrt(count) * 0.6)))
    parameters = {"min_cluster_size": minimum, "min_samples": min(8, max(3, minimum // 2)),
                  "metric": "euclidean", "cluster_selection_method": "eom", "n_jobs": 2}
    labels = HDBSCAN(**parameters).fit_predict(working)
    topics, used = [], set()
    all_tags = {}
    for index in indices:
        for tag in set(notes[index]["tags"]):
            all_tags[tag] = all_tags.get(tag, 0) + 1
    for group in sorted(set(labels) - {-1}):
        members = np.flatnonzero(labels == group)
        center = matrix[members].mean(axis=0)
        representatives = members[np.argsort(-(matrix[members] @ center), kind="stable")[:3]]
        tags = {}
        for member in members:
            for tag in set(notes[indices[member]]["tags"]):
                tags[tag] = tags.get(tag, 0) + 1
        ranked = sorted(tags, key=lambda tag: (-tags[tag] / len(members) * math.log1p(count / all_tags[tag]), tag))
        name = next((tag for tag in ranked if tag not in used), "")
        if not name:
            name = notes[indices[representatives[0]]]["title"][:22]
        used.add(name)
        cohesion = (float(np.dot(center, center)) * len(members) - 1) / (len(members) - 1)
        topics.append({"id": int(group), "label": name, "count": len(members),
                       "representatives": [int(indices[i]) for i in representatives],
                       "cohesion": round(cohesion, 5), "tags": ranked[:4]})
    return labels, topics, {"space": space, **parameters}


def metric_context(matrix, seed, sample_limit=512, required=()):
    count = len(matrix)
    main = np.sort(np.random.default_rng(seed).choice(count, min(sample_limit, count), replace=False))
    sample = np.concatenate([main, np.asarray(sorted(set(required) - set(main)), dtype=int)])
    k = min(10, max(1, (count - 1) // 2))
    distances = np.clip(1 - matrix[sample] @ matrix.T, 0, 2)
    distances[np.arange(len(sample)), sample] = np.inf
    order = np.argsort(distances, axis=1, kind="stable")
    ranks = np.empty_like(order)
    np.put_along_axis(ranks, order, np.arange(count)[None, :], axis=1)
    return {"sample": sample, "main_size": len(main), "k": k, "order": order, "ranks": ranks}


def measure(coordinates, context, labels, shared, reference):
    count, k = len(coordinates), context["k"]
    if count < 3:
        return {"recall": None, "trustworthiness": None, "same_topic": None,
                "new_recall": None, "drift_median": 0.0, "drift_p95": 0.0,
                "k": k, "queries": count}
    sample = context["sample"]
    main_size = context["main_size"]
    distances = cdist(coordinates[sample], coordinates)
    distances[np.arange(len(sample)), sample] = np.inf
    near = np.argsort(distances, axis=1, kind="stable")[:, :k]
    original = context["order"][:, :k]
    recall = np.asarray([len(set(a) & set(b)) / k for a, b in zip(near, original, strict=True)])
    ranks = np.take_along_axis(context["ranks"], near, axis=1)
    penalties = np.maximum(0, ranks[:main_size] + 1 - k).sum()
    trust = 1 - 2 * penalties / (main_size * k * (2 * count - 3 * k - 1))
    clustered = (labels[sample] >= 0) & (np.arange(len(sample)) < main_size)
    purity = np.mean(labels[near[clustered]] == labels[sample[clustered], None]) if clustered.any() else None
    new = sample >= shared
    radius = max(1e-10, float(np.percentile(np.linalg.norm(reference - reference.mean(axis=0), axis=1), 90)))
    drift = np.linalg.norm(coordinates[:shared] - reference, axis=1) / radius
    return {
        "recall": round(float(recall[:main_size].mean()), 5), "trustworthiness": round(float(trust), 5),
        "same_topic": None if purity is None else round(float(purity), 5),
        "new_recall": round(float(recall[new].mean()), 5) if new.any() else None,
        "drift_median": round(float(np.median(drift)), 6),
        "drift_p95": round(float(np.percentile(drift, 95)), 6),
        "k": k, "queries": main_size, "new_queries": int(new.sum()),
    }


def rounded(coordinates):
    if not np.isfinite(coordinates).all():
        raise ValueError("算法产生了非有限坐标")
    return np.round(coordinates, 7).tolist()


def export_html(output, bundle):
    """Bundle the page and actual note positions for opening directly in a browser."""
    directory = Path(__file__).resolve().parent
    output = Path(output)
    content = report(bundle)
    serialized = json.dumps(bundle, ensure_ascii=False, allow_nan=False).replace("<", "\\u003c")
    report_json = json.dumps(content, ensure_ascii=False).replace("<", "\\u003c")
    script = (directory / "app.js").read_text(encoding="utf-8")
    script = re.sub(r"import \{ StarScene \} from '[^']+';\n", "", script)
    scene = (directory / "scene.js").read_text(encoding="utf-8").replace("export class StarScene", "class StarScene")
    script = scene + "\n" + script.split("// BOOT")[0] + (
        "\n$('#import-open').hidden = true;\n"
        f"$('#report').href = URL.createObjectURL(new Blob([{report_json}], {{type:'text/markdown'}}));\n"
        f"display({serialized});\n"
    )
    html = (directory / "index.html").read_text(encoding="utf-8")
    html = re.sub(r'<link rel="stylesheet" href="/style.css[^"]*">', lambda _: "<style>" + (directory / "style.css").read_text() + "</style>", html)
    html = re.sub(r'<script type="module" src="/app.js[^"]*"></script>', lambda _: "<script type=\"module\">" + script + "</script>", html)
    (output / "stars.html").write_text(html, encoding="utf-8")


def build(notes, vectors, source, output, sizes=(50, 300), seed=42, progress=lambda text: None):
    output = Path(output)
    notes, matrix, valid, source = validate(notes, vectors, source)
    # Hash ordering gives nested samples without choosing convenient topics.
    order = sorted(range(len(valid)), key=lambda i: hashlib.sha256(f"{seed}:{notes[valid[i]]['name']}".encode()).digest())
    matrix, valid = matrix[order], [valid[i] for i in order]
    source["seed"] = seed
    source["sampling"] = "按 SHA-256(seed:name) 排序的嵌套样本；新增笔记取各样本末尾，非时间切分"
    valid_set = set(valid)
    bundle = {"schema": 1, "source": source, "notes": notes, "scales": [], "excluded": [i for i in range(len(notes)) if i not in valid_set]}
    dependencies = ["numpy", "scipy", "scikit-learn", "numba", "umap-learn", "pacmap", "annoy", "pynndescent"]
    bundle["versions"] = {name: importlib.metadata.version(name) for name in dependencies}
    source["built_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    counts = sorted(set([min(len(valid), n) for n in sizes] + [len(valid)]))
    for count in counts:
        progress(f"{count} 条：识别主题")
        data, indices = matrix[:count], valid[:count]
        addition = min(20, max(1, round(count * 0.05))) if count > 3 else 0
        shared = count - addition
        labels, topics, cluster_parameters = clusters(data, notes, indices)
        context = metric_context(data, seed, required=range(shared, count))
        before_context = metric_context(data[:shared], seed)
        semantic_neighbors = NearestNeighbors(n_neighbors=min(8, count), metric="cosine", algorithm="brute", n_jobs=2).fit(data)
        distances, neighbors = semantic_neighbors.kneighbors(data)
        peers = []
        for i, (row, scores) in enumerate(zip(neighbors, distances, strict=True)):
            peers.append([{"index": indices[int(j)], "similarity": round(float(1 - score), 5)}
                          for j, score in zip(row, scores, strict=True) if i != j][:7])
        semantic_silhouette = None
        labelled = labels >= 0
        if len(topics) > 1 and labelled.sum() > len(topics):
            semantic_silhouette = float(silhouette_score(data[labelled], labels[labelled], metric="cosine",
                                                        sample_size=min(1000, int(labelled.sum())), random_state=seed))
        scale = {"count": count, "base_count": shared, "added_count": addition, "indices": indices,
                 "labels": labels.tolist(), "topics": topics, "cluster_parameters": cluster_parameters,
                 "outliers": int(np.sum(labels == -1)), "neighbors": peers,
                 "semantic_silhouette": semantic_silhouette, "layouts": {}}
        for method in METHODS:
            progress(f"{count} 条：{method.upper()} 基础布局")
            started = time.perf_counter()
            model, before, parameters = layout(method, data[:shared], seed)
            fitted_seconds = time.perf_counter() - started
            progress(f"{count} 条：{method.upper()} 加入 {addition} 条后重算")
            started = time.perf_counter()
            _, after, _ = layout(method, data, seed)
            refitted_seconds = time.perf_counter() - started
            after = align(before, after, shared)
            progress(f"{count} 条：{method.upper()} 固定旧点加入 {addition} 条")
            started = time.perf_counter()
            frozen, mechanism = incremental(model, parameters, data, shared, before)
            transform_seconds = time.perf_counter() - started
            # Every stage uses the same baseline frame, preserving measurable displacement.
            center = before.mean(axis=0)
            radius = max(1e-8, float(np.percentile(np.linalg.norm(before - center, axis=1), 90)))
            before, after, frozen = [(coordinates - center) / radius for coordinates in (before, after, frozen)]
            scale["layouts"][method] = {
                "parameters": parameters, "incremental": mechanism,
                "stages": {
                    "base": {"coordinates": rounded(before), "seconds": round(fitted_seconds, 3),
                             "metrics": measure(before, before_context, labels[:shared], shared, before)},
                    "refit": {"coordinates": rounded(after), "seconds": round(refitted_seconds, 3),
                              "metrics": measure(after, context, labels, shared, before)},
                    "frozen": {"coordinates": rounded(frozen), "seconds": round(transform_seconds, 3),
                               "metrics": measure(frozen, context, labels, shared, before)},
                },
            }
        bundle["scales"].append(scale)
    from semantics import refine_bundle

    refine_bundle(bundle, matrix, valid, progress)
    from tuning import tune_bundle

    tune_bundle(bundle, matrix, valid, progress)
    progress("写入布局和比较报告")
    write_json(output / "bundle.json", bundle)
    (output / "comparison.md").write_text(report(bundle), encoding="utf-8")
    export_html(output, bundle)
    return bundle


def report(bundle):
    source = bundle["source"]
    kind = {"personal": "个人笔记", "demo": "既有演示语料", "evaluation": "验证材料"}.get(source["kind"], source["kind"])
    lines = [
        "# 群星视觉与语义比较", "",
        f"数据：{source['label']}（{kind}），{source['vector_notes']} 条有效向量，{source['dimensions']} 维。",
        f"数据指纹：`{source['fingerprint']}`。随机种子：{source['seed']}。",
        f"采样：{source['sampling']}。每个规模留出约 5% 且最多 20 条作为新增笔记。",
        "主题通过独立的 10 维 UMAP 与 HDBSCAN Leaf 识别，再在原向量空间核验亲和度与近邻支持；三种二维布局共用成员关系，-1 表示独立笔记。",
        "表中近邻指标来自原向量余弦近邻和二维欧氏近邻，每个规模固定抽取至多 512 个查询点，参考库包含该规模全部点。新增点近邻指标覆盖全部留出笔记。",
        "旧点位移先做相似变换对齐，消除平移、旋转、镜像和缩放，以基础布局的 P90 半径为单位。",
        "固定旧点的位移为零是放置策略的约束；新增点近邻保持率用于检查这项约束的代价。",
        "新增样本是散列留出，反映同分布新增笔记，不覆盖新主题涌现或模型空间变更。", "",
    ]
    pct = lambda value: "—" if value is None else f"{value:.1%}"
    for scale in bundle["scales"]:
        lines += [f"## {scale['count']} 条笔记", "",
                  "| 规模（基础 + 新增） | 布局 | 近邻保持 | Trustworthiness | 同群近邻 | 重算旧点 P95 位移 | 固定旧点后新增点近邻保持 |",
                  "| --- | --- | --- | --- | --- | --- | --- |"]
        for method in ("umap", "pacmap", "pca"):
            stages = scale["layouts"][method]["stages"]
            full, frozen = stages["refit"]["metrics"], stages["frozen"]["metrics"]
            lines.append(f"| {scale['base_count']} + {scale['added_count']} | {method.upper()} | "
                         f"{pct(full['recall'])} | {pct(full['trustworthiness'])} | {pct(full['same_topic'])} | "
                         f"{pct(full['drift_p95'])} | {pct(frozen['new_recall'])} |")
        lines += ["", f"{scale['count']} 条：{len(scale['topics'])} 个主题，{scale['outliers']} 条独立笔记；{scale['cluster_parameters']['space']}。"]
        if scale.get("cluster_candidates"):
            lines += ["", "| 聚类策略 | 主题 | 独立笔记 | 原标签一致率 | 标签 NMI |",
                      "| --- | --- | --- | --- | --- |"]
            for candidate in scale["cluster_candidates"]:
                nmi = "—" if candidate["tag_nmi"] is None else f"{candidate['tag_nmi']:.3f}"
                lines.append(f"| {candidate['name']} | {candidate['clusters']} | {candidate['outliers']} | "
                             f"{pct(candidate['tag_purity'])} | {nmi} |")
            lines += ["", "原标签一致率为各组最常见标签所占的已归类笔记比例。标签只参与命名与辅助评估；各策略覆盖的笔记不同。", ""]
        for topic in scale["topics"]:
            representatives = "；".join(bundle["notes"][i]["title"] for i in topic["representatives"])
            lines.append(f"- {topic['label']}（{topic['count']} 条，组内平均余弦 {topic['cohesion']:.3f}）：{representatives}")
        lines.append("")
    full = bundle["scales"][-1]
    best = max(METHODS, key=lambda method: full["layouts"][method]["stages"]["refit"]["metrics"]["recall"] or 0)
    if full.get("previous_clustering"):
        old = full["previous_clustering"]
        lines += [
            "## 聚类判断", "",
            f"同一批 {full['count']} 条笔记，主题由 {old['topics']} 个变为 {len(full['topics'])} 个，"
            f"独立笔记由 {old['outliers']} 条变为 {full['outliers']} 条；二维展示坐标保持原值。",
            "高维密度估计与群选择策略对结果影响很大。减少独立笔记不能单独作为优化目标："
            "放宽参数或采用 EOM 可能把多个领域合成大群，Leaf 在可读性与覆盖之间更适合这批数据。",
            "小样本缺少足够的同主题近邻，组合主题比单领域命名更稳妥。语义过渡处的独立笔记继续保留。",
            "",
        ]
    lines += [
        "## 选型判断", "",
        "- PCA：作为可解释、低成本、可固定基底的基线；主题可能挤在一起，局部语义近邻损失需要结合表中指标判断。",
        "- UMAP：优先考察局部邻域和主题可读性；二维岛屿的间距不等于主题的语义距离，完整重算可能改变地图结构。",
        "- PaCMAP：优先考察主题之间的连续过渡和局部细节；疏密与留白仍受参数影响，完整重算也需要位移评估。",
        f"- 视觉设计候选：优先 {best.upper()}，其全量近邻保持率最高；保留另一种非线性方法作视觉对照，PCA 作基线。",
        "- 稳定性方案：冻结已发布坐标，用各算法的 transform 放置新笔记；累计新增主题后另行评估重建布局的必要性。", "",
        f"全量聚类保留 {full['outliers'] / full['count']:.1%} 独立笔记。若比例较高或代表笔记跨越多个领域，应继续调整聚类参数和主题命名。",
        "同群近邻和高维轮廓系数衡量向量聚类与布局的一致性，主题是否合理需要阅读代表笔记判断。",
    ]
    if bundle.get("visual_version") == 2:
        lines += [
            "", "## 视觉实现", "",
            "- A 精致星图：清晰的星核、克制的光晕与主题文字，适合观察成员结构和阅读。",
            "- B 天文摄影：同一组星体增加密度辉光与亮度对比；辉光按实际笔记的二维位置等权累积，缓存成离屏纹理。",
            "- 两种视觉共用坐标、主题与星体属性，切换不会触发布局计算或改变相机。",
            "- 星体尺寸与亮度结合原向量主题中心性、近邻密度、独特性和资源 ID 散列；同一笔记在不同规模中共用属性。",
            "- 远景限制主题名称数量，中景渐入局部主题，近景显示经过避让的笔记摘录；绘制按交互触发，无持续动画。",
            f"- 坐标指纹：`{bundle.get('coordinate_fingerprint', '')}`。",
        ]
    if bundle.get("umap_presets"):
        lines += ["", "## UMAP 精调", "",
                  "| 版本 | n_neighbors | min_dist | spread | 全量近邻保持 | 同群近邻 | 新增点近邻保持 |",
                  "| --- | --- | --- | --- | --- | --- | --- |"]
        for preset in bundle["umap_presets"]:
            variant = full["umap_variants"][preset["id"]]
            metrics = variant["stages"]["frozen"]["metrics"]
            p = variant["parameters"]
            lines.append(f"| {preset['label']} | {p.get('n_neighbors', '—')} | {p.get('min_dist', '—')} | "
                         f"{p.get('spread', 1)} | {pct(metrics['recall'])} | {pct(metrics['same_topic'])} | {pct(metrics['new_recall'])} |")
        lines += ["", "各版本使用同一批向量、主题、星体属性与渲染。坐标只做相似变换对齐；"
                  "主体取景根据分位数与最大裁切比例选择相机范围，不变更笔记位置。"]
        lines += ["", "氛围分为星点小光晕、局部密度柔光和极淡背景雾。密度使用浮点高斯累积，"
                  "仅超过孤立星体阈值的区域产生柔光，避免整片蓝雾。"
                  "星核与文字位于氛围层之上，主题使用低饱和蓝、灰紫、蓝青，少量特殊星使用暖白与淡金。"]
    if source["kind"] != "personal":
        lines += ["", "个人笔记的正式选型需要在个人索引上运行同一流程。演示语料用于比较算法行为。"]
    if bundle.get("embedding_models"):
        lines += ["", "## 向量模型对照", "",
                  "同一批笔记与 UMAP 参数，主题成员、星体属性、渲染和取景规则固定。"
                  "近邻保持率以各自模型的原向量计算；相同维数的模型也可直接对照。",
                  "", "| 模型 | 版本 | 全量近邻保持 | 近邻可信度 | 新增点近邻保持 |",
                  "| --- | --- | --- | --- | --- |"]
        for model in bundle["embedding_models"]:
            variants = full["umap_variants"] if model["id"] == "minilm384" else full["embedding_variants"][model["id"]]["umap_variants"]
            for preset in bundle["umap_presets"]:
                m = variants[preset["id"]]["stages"]["frozen"]["metrics"]
                lines.append(f"| {model['label']} | {preset['label']} | {pct(m['recall'])} | {pct(m['trustworthiness'])} | {pct(m['new_recall'])} |")
        comparisons = full.get("embedding_neighbor_comparisons", [
            {"left": "minilm384", "right": identifier, "overlap_at_7": data["source_neighbor_overlap_at_7"]}
            for identifier, data in full.get("embedding_variants", {}).items()
        ])
        labels = {model["id"]: model["label"] for model in bundle["embedding_models"]}
        for pair in comparisons:
            lines += ["", f"{labels[pair['left']]} / {labels[pair['right']]}：原向量 top-7 近邻平均重合 "
                      f"{pct(pair['overlap_at_7'])}。"]
        lines += ["", "主题成员固定为 MiniLM 基准，切换模型时共用。"]
    return "\n".join(lines) + "\n"


def build_limited(*args, **kwargs):
    with threadpool_limits(limits=2):
        return build(*args, **kwargs)
