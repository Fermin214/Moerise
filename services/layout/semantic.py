"""Themes and stable stellar attributes derived from source embeddings."""

import hashlib
import math
from collections import Counter


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def neighborhoods(matrix):
    import numpy as np
    from sklearn.neighbors import NearestNeighbors

    count = len(matrix)
    if not count:
        return np.empty((0, 0), dtype=int), np.empty((0, 0))
    distance, indices = NearestNeighbors(n_neighbors=min(8, count), metric="cosine",
                                        algorithm="brute").fit(matrix).kneighbors(matrix)
    rows, scores = [], []
    for i, (row, values) in enumerate(zip(indices, distance)):
        keep = row != i
        rows.append(row[keep][:7])
        scores.append(1 - values[keep][:7])
    return np.asarray(rows), np.asarray(scores)


def _doc_features(document):
    tags = {}
    for raw in document.get("tags") or ():
        text = str(raw).strip()
        if text:
            tags.setdefault(text.casefold(), text)
    terms = {}
    for raw in document.get("keywords") or ():
        text = str(raw).strip()
        if text:
            terms.setdefault(text.casefold(), text)
    return tags, terms


def _pick_distinctive(candidates):
    selected = []
    for _, _, _, is_tag, label in candidates:
        norm = label.casefold()
        if any(norm in item[0] or item[0] in norm for item in selected):
            continue
        selected.append((norm, is_tag, label))
        if len(selected) == 2:
            break
    if not selected:
        return ""
    sep = " / " if all(item[1] for item in selected) else " · "
    return sep.join(item[2] for item in selected)[:100]


def theme_label(documents, members, fallback, scope=None, class_members=None):
    scope_list = list(range(len(documents))) if scope is None else list(dict.fromkeys(scope))
    scope_set = set(scope_list)
    target = [i for i in dict.fromkeys(members) if i in scope_set]
    scope_size = len(scope_list)
    cluster_size = len(target)
    if not scope_size or not cluster_size:
        return fallback
    target_set = set(target)
    features = {i: _doc_features(documents[i]) for i in scope_list}
    df = Counter()
    in_tags = Counter()
    in_terms = Counter()
    display_tag = {}
    display_term = {}
    for i in scope_list:
        tags, terms = features[i]
        for norm in tags.keys() | terms.keys():
            df[norm] += 1
        if i in target_set:
            for norm, text in tags.items():
                in_tags[norm] += 1
                display_tag.setdefault(norm, text)
            for norm, text in terms.items():
                in_terms[norm] += 1
                display_term.setdefault(norm, text)
    classes = [target]
    assigned = set(target)
    for cls in class_members or ():
        extra = [i for i in cls if i in scope_set and i not in assigned]
        if extra:
            classes.append(extra)
            assigned.update(extra)
    remainder = [i for i in scope_list if i not in assigned]
    if remainder:
        classes.append(remainder)
    cf = Counter()
    class_totals = []
    for cls in classes:
        total = 0
        for i in cls:
            tags, terms = features[i]
            tokens = tags.keys() | terms.keys()
            total += len(tokens)
            for norm in tokens:
                cf[norm] += 1
        class_totals.append(total)
    avg_tokens = sum(class_totals) / max(1, len(classes))
    outside_size = scope_size - cluster_size
    log_denom = max(1.0, math.log(scope_size + 1.0))
    scored = []
    fallback_candidates = []
    for norm in in_tags.keys() | in_terms.keys():
        in_count = len([1 for i in target if norm in features[i][0] or norm in features[i][1]])
        if not in_count:
            continue
        is_tag = in_tags[norm] > 0 and norm in display_tag
        label = display_tag[norm] if is_tag else display_term[norm]
        tf = in_count / cluster_size
        global_df = df[norm]
        out_count = global_df - in_count
        ubiquitous = (outside_size > 0 and global_df / scope_size >= 0.6 and
                      (out_count / outside_size) * 1.15 >= tf)
        if not ubiquitous and not (len(classes) >= 2 and global_df / scope_size >= 0.85):
            ctfidf = tf * math.log(1.0 + avg_tokens / max(1.0, cf[norm]))
            corpus_idf = max(0.0, math.log((scope_size + 1.0) / (global_df + 0.5))) / log_denom
            score = ctfidf * corpus_idf * (1.25 if is_tag else 1.0)
            if score > 0:
                scored.append((score, in_count, len(label), is_tag, label))
        if global_df / scope_size < 0.85 or len(classes) == 1:
            fallback_candidates.append((tf, in_count, len(label), is_tag, label))
    scored.sort(key=lambda item: (-item[0], -item[1], -item[2], item[4]))
    picked = _pick_distinctive(scored)
    if picked:
        return picked
    if len(classes) == 1:
        fallback_candidates.sort(key=lambda item: (-item[0], -item[1], -item[2], item[4]))
        picked = _pick_distinctive(fallback_candidates)
        if picked:
            return picked
    return fallback


def cluster(matrix, nearest):
    import numpy as np
    from sklearn.cluster import HDBSCAN
    from umap import UMAP

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


def describe(matrix, documents, coordinates, previous=None, refresh_labels=False):
    import numpy as np
    from sklearn.cluster import HDBSCAN

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
    theme_classes = [np.flatnonzero(labels == group_index).tolist() for group_index in range(len(groups))]
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
        member_list = members.tolist()
        label = None if refresh_labels else group.get("label")
        theme = {"id": group["id"], "label": label or theme_label(
                     documents, member_list, "未命名主题", class_members=theme_classes),
                 "x": float(position[0]), "y": float(position[1]),
                 "members": [documents[i]["name"] for i in members]}
        districts = []
        if previous:
            parent = set(theme["members"])
            district_classes = [[names[name] for name in district["members"] if name in parent]
                                for district in group.get("districts", [])]
            for district, local in zip(group.get("districts", []), district_classes):
                if local:
                    at = coordinates[local].mean(axis=0)
                    label = district["label"] if not refresh_labels else theme_label(
                        documents, local, theme["label"], scope=member_list,
                        class_members=[members for members in district_classes if members])
                    districts.append(dict(district, label=label, members=[documents[i]["name"] for i in local],
                                          x=float(at[0]), y=float(at[1])))
        elif manifold is not None and len(members) >= 32:
            children = HDBSCAN(min_cluster_size=max(5, min(12, len(members) // 9)),
                               min_samples=3, cluster_selection_method="leaf", n_jobs=2).fit_predict(manifold[members])
            child_ids = sorted(set(children) - {-1})
            district_classes = [members[children == child].tolist() for child in child_ids]
            for child in child_ids:
                local = members[children == child]
                at = coordinates[local].mean(axis=0)
                districts.append({"id": "district-" + digest(documents[int(local[0])]["name"])[:16],
                                  "label": theme_label(documents, local.tolist(), theme["label"],
                                                       scope=member_list, class_members=district_classes),
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
