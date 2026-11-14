"""Semantic hierarchy and reproducible stellar attributes, independent of 2D positions."""

import hashlib
import json
import math
from collections import Counter

import numpy as np
from sklearn.cluster import HDBSCAN
from sklearn.decomposition import PCA
from sklearn.metrics import normalized_mutual_info_score, silhouette_score
from sklearn.neighbors import NearestNeighbors


def digest(value):
    return int.from_bytes(hashlib.sha256(value.encode()).digest()[:4], "big") / 2**32


def name_groups(matrix, labels, notes, indices, prefix=""):
    topics, used = [], set()
    totals = Counter(tag for index in indices for tag in set(notes[index]["tags"]))
    for group in sorted(set(labels) - {-1}):
        members = np.flatnonzero(labels == group)
        center = matrix[members].mean(axis=0)
        representatives = members[np.argsort(-(matrix[members] @ center), kind="stable")[:3]]
        local = Counter(tag for i in members for tag in set(notes[indices[i]]["tags"]))
        primary = Counter(notes[indices[i]]["tags"][0] for i in members if notes[indices[i]]["tags"])
        ordered = sorted(local, key=lambda tag: (-local[tag] / len(members) * math.log1p(len(matrix) / totals[tag]), tag))
        primary_order = primary.most_common()
        dominant = primary_order[0][0] if primary_order else ""
        if dominant:
            label = dominant
            if len(primary_order) > 1 and (primary_order[0][1] / len(members) < .55
                                          or primary_order[1][1] / len(members) > .26):
                label += " / " + primary_order[1][0]
            if label in used:
                detail = next((tag for tag in ordered if tag != dominant), "")
                label = dominant + (" · " + detail if detail else " · " + notes[indices[representatives[0]]]["title"][:9])
        else:
            label = notes[indices[representatives[0]]]["title"][:18]
        used.add(label)
        cohesion = (float(np.dot(center, center)) * len(members) - 1) / max(1, len(members) - 1)
        topics.append({
            "id": int(group), "label": label, "count": len(members),
            "representatives": [int(indices[i]) for i in representatives],
            "cohesion": round(cohesion, 5), "tags": ordered[:5],
            "tag_agreement": round(primary_order[0][1] / len(members), 4) if primary_order else None,
            "members": [int(i) for i in members],
        })
    return topics


def assessment(matrix, labels, notes, indices):
    groups = sorted(set(labels) - {-1})
    valid = labels >= 0
    tags = np.asarray([notes[i]["tags"][0] if notes[i]["tags"] else "" for i in indices])
    supported = valid & (tags != "")
    purity = sum(Counter(tags[(labels == group) & (tags != "")]).most_common(1)[0][1]
                 for group in groups if np.any((labels == group) & (tags != ""))) / max(1, supported.sum())
    nmi = normalized_mutual_info_score(tags[supported], labels[supported]) if supported.any() else None
    return {"clusters": len(groups), "outliers": int(np.sum(~valid)),
            "tag_purity": round(purity, 5), "tag_nmi": None if nmi is None else round(float(nmi), 5)}


def refine_scale(matrix, notes, indices, previous, seed):
    from umap import UMAP

    count = len(matrix)
    if count < 16 or np.max(np.std(matrix, axis=0)) < 1e-10:
        labels = np.asarray(previous["labels"])
        return labels, previous["topics"], [], previous["cluster_parameters"], []
    # Density in 50+ dimensions is too diffuse for this corpus. Keep clustering
    # separate from the displayed 2D projection by using a 10D semantic manifold.
    parameters = {"n_components": min(10, count - 2), "n_neighbors": min(30, count - 1),
                  "min_dist": 0.0, "metric": "cosine", "random_state": seed, "n_jobs": 1}
    manifold = UMAP(**parameters).fit_transform(matrix.astype(np.float32))
    minimum = max(5, min(24, round(math.sqrt(count) * .6)))
    samples = min(8, max(3, minimum // 2))
    candidates = []
    pc_count = min(50, count - 1, matrix.shape[1])
    pca = PCA(n_components=pc_count, svd_solver="full").fit_transform(matrix)
    original = previous["cluster_parameters"]
    for name, values, size, minimum_samples, selection in [
        ("PCA + 原密度参数", pca, original["min_cluster_size"], original["min_samples"], "eom"),
        ("PCA + 放宽密度参数", pca, max(5, minimum // 2), 3, "eom"),
        ("UMAP 10D + EOM", manifold, minimum, samples, "eom"),
        ("UMAP 10D + Leaf", manifold, minimum, samples, "leaf"),
    ]:
        found = HDBSCAN(min_cluster_size=size, min_samples=minimum_samples,
                        cluster_selection_method=selection).fit_predict(values)
        candidates.append({"name": name, "min_cluster_size": size, "min_samples": minimum_samples,
                           "cluster_selection_method": selection,
                           **assessment(matrix, found, notes, indices)})
        if selection == "leaf":
            labels = found
    # Verify projected groups against the source embedding, leaving weak fits alone.
    _, nearest = NearestNeighbors(n_neighbors=min(8, count), metric="cosine", algorithm="brute").fit(matrix).kneighbors(matrix)
    guarded = labels.copy()
    for group in sorted(set(labels) - {-1}):
        members = np.flatnonzero(labels == group)
        center = matrix[members].mean(axis=0)
        center /= max(1e-12, np.linalg.norm(center))
        affinity = matrix[members] @ center
        support = np.mean(labels[nearest[members, 1:]] == group, axis=1)
        guarded[members[(affinity < .35) | (support < .25)]] = -1
    labels = guarded
    topics = name_groups(matrix, labels, notes, indices)
    subtopics = []
    for group in sorted(set(labels) - {-1}):
        members = np.flatnonzero(labels == group)
        if len(members) < 32:
            continue
        children = HDBSCAN(min_cluster_size=max(5, min(12, len(members) // 9)),
                           min_samples=3, cluster_selection_method="leaf").fit_predict(manifold[members])
        if len(set(children) - {-1}) < 2:
            continue
        named = name_groups(matrix[members], children, notes, [indices[i] for i in members])
        for child in named:
            child["parent"] = int(group)
            child["id"] = f"{group}/{child['id']}"
            child["members"] = [int(members[i]) for i in child["members"]]
            # Repeated broad labels make poor local names. Prefer a distinct tag.
            parent = next(topic for topic in topics if topic["id"] == group)
            local_tag = next((tag for tag in child["tags"] if tag not in parent["label"]), None)
            if local_tag:
                child["label"] = local_tag
            subtopics.append(child)
    candidates.append({"name": "UMAP 10D + Leaf + 原向量核验", "min_cluster_size": minimum,
                       "min_samples": samples, **assessment(matrix, labels, notes, indices)})
    return labels, topics, subtopics, {
        "space": "归一化原向量 → UMAP 10 维 → HDBSCAN Leaf → 原向量亲和度与近邻核验",
        "manifold": parameters, "min_cluster_size": minimum, "min_samples": samples,
        "cluster_selection_method": "leaf",
        "affinity_min": .35, "neighbor_support_min": .25,
        "naming": "标签描述聚类成员，不参与聚类；多领域群保留组合名称",
    }, candidates


def stellar_attributes(matrix, notes, indices, labels, topics):
    count = len(matrix)
    distances, _ = NearestNeighbors(n_neighbors=min(8, count), metric="cosine", algorithm="brute").fit(matrix).kneighbors(matrix)
    density = np.mean(1 - distances[:, 1:], axis=1) if count > 1 else np.ones(1)
    order = np.argsort(np.argsort(density, kind="stable"), kind="stable") / max(1, count - 1)
    centrality = np.zeros(count)
    cores = set()
    for group in topics:
        members = np.flatnonzero(labels == group["id"])
        center = matrix[members].mean(axis=0)
        scores = matrix[members] @ center
        centrality[members] = np.argsort(np.argsort(scores, kind="stable"), kind="stable") / max(1, len(members) - 1)
        cores.add(int(members[np.argmax(scores)]))
    isolates = set(int(i) for i in np.argsort(density, kind="stable")[:max(1, round(count * .012))])
    attributes = []
    for i, index in enumerate(indices):
        seed = digest(notes[index]["name"])
        role = "core" if i in cores else "isolated" if i in isolates else "ordinary"
        luminosity = (.19 + .65 * seed**2) * (.72 + .15 * order[i] + .13 * centrality[i])
        radius = .43 + .59 * seed**3 + .20 * centrality[i]
        if role == "core":
            luminosity, radius = .98, 1.7
        elif role == "isolated":
            luminosity, radius = .83, 1.25
        attributes.append({
            "seed": round(seed, 6), "role": role,
            "luminosity": round(luminosity, 5), "radius": round(radius, 5),
            "centrality": round(float(centrality[i]), 4),
            "local_similarity": round(float(density[i]), 4),
            "uniqueness": round(float(1 - density[i]), 4),
        })
    return attributes


def refine_bundle(bundle, matrix, valid, progress=print):
    """Preserve all published coordinate arrays while improving semantic metadata."""
    from engine import measure, metric_context

    by_index = {index: matrix[i] for i, index in enumerate(valid)}
    for scale in bundle["scales"]:
        progress(f"{scale['count']} 条：改善主题与局部主题")
        data = np.asarray([by_index[i] for i in scale["indices"]])
        labels, topics, subtopics, parameters, candidates = refine_scale(
            data, bundle["notes"], scale["indices"], scale, bundle["source"]["seed"])
        scale["previous_clustering"] = {
            "outliers": scale["outliers"], "topics": len(scale["topics"]),
            "parameters": scale["cluster_parameters"],
        }
        scale.update(labels=labels.tolist(), topics=topics, subtopics=subtopics,
                     cluster_parameters=parameters, cluster_candidates=candidates,
                     outliers=int(np.sum(labels == -1)))
        valid_labels = labels >= 0
        scale["semantic_silhouette"] = (
            float(silhouette_score(data[valid_labels], labels[valid_labels], metric="cosine",
                                   sample_size=min(1000, int(valid_labels.sum())), random_state=42))
            if len(topics) > 1 and valid_labels.sum() > len(topics) else None)
        shared = scale["base_count"]
        contexts = {
            "base": metric_context(data[:shared], 42),
            "full": metric_context(data, 42, required=range(shared, len(data))),
        }
        for layout in scale["layouts"].values():
            reference = np.asarray(layout["stages"]["base"]["coordinates"])
            for stage, value in layout["stages"].items():
                value["metrics"] = measure(np.asarray(value["coordinates"]),
                    contexts["base" if stage == "base" else "full"],
                    labels[:shared] if stage == "base" else labels, shared, reference)
    full = bundle["scales"][-1]
    full_matrix = np.asarray([by_index[i] for i in full["indices"]])
    attributes = stellar_attributes(full_matrix, bundle["notes"], full["indices"],
                                   np.asarray(full["labels"]), full["topics"])
    by_note = dict(zip(full["indices"], attributes))
    for scale in bundle["scales"]:
        scale["star_attributes"] = [by_note[index] for index in scale["indices"]]
    bundle["visual_version"] = 2
    bundle["source"]["stellar_attributes"] = "完整语料的原向量中心性、近邻密度、独特性与资源 ID 散列；各规模共用属性"
    bundle["coordinate_fingerprint"] = hashlib.sha256(json.dumps(
        [{method: {stage: value["coordinates"] for stage, value in layout["stages"].items()}
          for method, layout in scale["layouts"].items()} for scale in bundle["scales"]],
        sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return bundle
