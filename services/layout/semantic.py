"""Themes and stable stellar attributes derived from source embeddings."""

import hashlib
import math
from collections import Counter

import numpy as np
from sklearn.cluster import HDBSCAN
from sklearn.neighbors import NearestNeighbors
from umap import UMAP


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def neighborhoods(matrix):
    count = len(matrix)
    if not count:
        return np.empty((0, 0), dtype=int), np.empty((0, 0))
    distance, indices = NearestNeighbors(n_neighbors=min(8, count), metric="cosine",
                                        algorithm="brute").fit(matrix).kneighbors(matrix)
    # Remove the query by identity; equal vectors can appear before the query.
    rows, scores = [], []
    for i, (row, values) in enumerate(zip(indices, distance)):
        keep = row != i
        rows.append(row[keep][:7])
        scores.append(1 - values[keep][:7])
    return np.asarray(rows), np.asarray(scores)


def theme_label(documents, members, fallback):
    tags = Counter(tag for i in members for tag in (documents[i].get("tags") or []))
    terms = Counter(word for i in members for word in (documents[i].get("keywords") or []))
    selected = sorted(tags, key=lambda tag: (-tags[tag], tag))[:2]
    if selected:
        return " / ".join(selected)[:100]
    selected = sorted(terms, key=lambda word: (-terms[word], word))[:2]
    return " · ".join(selected)[:100] if selected else fallback


def cluster(matrix, nearest):
    count = len(matrix)
    labels = np.full(count, -1, dtype=int)
    if count < 16 or np.max(np.std(matrix, axis=0)) < 1e-7:
        return labels, None
    manifold = UMAP(n_components=min(10, count - 2), n_neighbors=min(30, count - 1),
                    min_dist=0, metric="cosine", random_state=42,
                    transform_seed=42, n_jobs=1).fit_transform(matrix)
    minimum = max(5, min(24, round(math.sqrt(count) * .6)))
    labels = HDBSCAN(min_cluster_size=minimum, min_samples=min(8, max(3, minimum // 2)),
                     cluster_selection_method="leaf", n_jobs=2).fit_predict(manifold)
    guarded = labels.copy()
    for group in sorted(set(labels) - {-1}):
        members = np.flatnonzero(labels == group)
        center = matrix[members].mean(axis=0)
        center /= max(1e-12, np.linalg.norm(center))
        affinity = matrix[members] @ center
        support = np.mean(labels[nearest[members]] == group, axis=1)
        guarded[members[(affinity < .35) | (support < .25)]] = -1
    return guarded, manifold


def describe(matrix, documents, coordinates, previous=None):
    count = len(matrix)
    nearest, similarities = neighborhoods(matrix)
    names = {document["name"]: i for i, document in enumerate(documents)}
    manifold = None
    if previous:
        labels = np.full(count, -1, dtype=int)
        groups = []
        for group in previous:
            members = [names[name] for name in group["members"] if name in names
                       and documents[names[name]].get("unchanged")]
            if not members:
                continue
            group = dict(group, members=members)
            labels[members] = len(groups)
            groups.append(group)
        centers = []
        for group in groups:
            center = matrix[group["members"]].mean(axis=0)
            centers.append(center / max(1e-12, np.linalg.norm(center)))
        # Preserve independent notes; classify only new or changed vectors.
        for i, document in enumerate(documents):
            if document.get("unchanged") or not centers:
                continue
            affinity = np.asarray(centers) @ matrix[i]
            group = int(np.argmax(affinity))
            support = np.mean(labels[nearest[i]] == group) if len(nearest[i]) else 0
            if affinity[group] >= .35 and support >= .25:
                labels[i] = group
    else:
        labels, manifold = cluster(matrix, nearest)
        groups = []
        for label in sorted(set(labels) - {-1}):
            members = np.flatnonzero(labels == label).tolist()
            center = matrix[members].mean(axis=0)
            representative = members[int(np.argmax(matrix[members] @ center))]
            groups.append({"id": "topic-" + digest(documents[representative]["name"])[:16],
                           "members": members})
        remap = {label: i for i, label in enumerate(sorted(set(labels) - {-1}))}
        labels = np.asarray([remap.get(int(label), -1) for label in labels])
    themes, attributes = [], []
    centrality = np.zeros(count)
    cores = set()
    for group_index, group in enumerate(groups):
        members = np.flatnonzero(labels == group_index)
        if not len(members):
            continue
        center = matrix[members].mean(axis=0)
        scores = matrix[members] @ center
        centrality[members] = np.argsort(np.argsort(scores, kind="stable"), kind="stable") / max(1, len(members) - 1)
        cores.add(int(members[np.argmax(scores)]))
        position = coordinates[members].mean(axis=0)
        theme = {"id": group["id"], "label": group.get("label") or theme_label(documents, members, "未命名主题"),
                 "x": float(position[0]), "y": float(position[1]),
                 "members": [documents[i]["name"] for i in members]}
        districts = []
        if previous:
            parent = set(theme["members"])
            for district in group.get("districts", []):
                local = [names[name] for name in district["members"] if name in parent]
                if local:
                    at = coordinates[local].mean(axis=0)
                    districts.append(dict(district, members=[documents[i]["name"] for i in local],
                                          x=float(at[0]), y=float(at[1])))
        elif manifold is not None and len(members) >= 32:
            children = HDBSCAN(min_cluster_size=max(5, min(12, len(members) // 9)),
                               min_samples=3, cluster_selection_method="leaf", n_jobs=2).fit_predict(manifold[members])
            for child in sorted(set(children) - {-1}):
                local = members[children == child]
                at = coordinates[local].mean(axis=0)
                districts.append({"id": "district-" + digest(documents[int(local[0])]["name"])[:16],
                                  "label": theme_label(documents, local, theme["label"]),
                                  "x": float(at[0]), "y": float(at[1]),
                                  "members": [documents[i]["name"] for i in local]})
        if districts:
            theme["districts"] = sorted(districts, key=lambda value: -len(value["members"]))[:6]
        themes.append(theme)
    density = similarities.mean(axis=1) if similarities.shape[1] else np.ones(count)
    order = np.argsort(np.argsort(density, kind="stable"), kind="stable") / max(1, count - 1)
    isolated = set(int(i) for i in np.argsort(density, kind="stable")[:max(1, round(count * .012))])
    for i, document in enumerate(documents):
        if document.get("unchanged") and document.get("stellar"):
            attributes.append(document["stellar"])
            continue
        seed = int(digest(document["name"])[:12], 16) / (16 ** 12 - 1)
        role = "core" if i in cores else "isolated" if i in isolated else "ordinary"
        luminosity = (.19 + .65 * seed ** 2) * (.72 + .15 * order[i] + .13 * centrality[i])
        radius = .43 + .59 * seed ** 3 + .20 * centrality[i]
        if role == "core":
            luminosity, radius = .98, 1.7
        elif role == "isolated":
            luminosity, radius = .83, 1.25
        attributes.append({"seed": round(seed, 6), "role": role,
                           "radius": round(radius, 5), "luminosity": round(luminosity, 5)})
    links, seen = [], set()
    for i, row in enumerate(nearest):
        for j, score in list(zip(row, similarities[i]))[:3]:
            pair = tuple(sorted((documents[i]["name"], documents[int(j)]["name"])))
            if score < .55 or pair in seen:
                continue
            seen.add(pair)
            links.append({"source": pair[0], "target": pair[1], "weight": round(float(np.clip(score, .55, 1)), 6)})
    return themes, attributes, links
