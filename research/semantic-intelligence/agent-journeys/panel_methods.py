"""Offline-only matched panel sampling and bounded inference. No dispatch code."""
import hashlib
import json
import math
from pathlib import Path

def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()).hexdigest()


def module_hash():
    return hashlib.sha256(Path(__file__).read_bytes()).hexdigest()


def validate_policy(policy):
    if policy.get("schema") != "offline-panel-policy-v1":
        raise ValueError("policy schema")
    strata = policy.get("strata")
    if (not isinstance(strata, list) or not strata or not all(isinstance(x, str) and x and "\0" not in x for x in strata) or len(set(strata)) != len(strata)):
        raise ValueError("strata schema")
    count = policy.get("tasks_per_stratum")
    if type(count) is not int or count < 1 or policy.get("tasks") != count * len(strata):
        raise ValueError("task counts")
    repeats = policy.get("repeats")
    if not isinstance(repeats, list) or not repeats or any(type(x) is not int or x < 1 for x in repeats) or len(set(repeats)) != len(repeats):
        raise ValueError("repeat identities")
    if policy.get("arms") != ["A", "B"] or policy.get("attempts") != policy["tasks"] * len(repeats) * 2:
        raise ValueError("matched arm counts")
    if (type(policy.get("alpha")) not in (int, float) or not 0 < policy["alpha"] < 1 or
        type(policy.get("margin")) not in (int, float) or not 0 < policy["margin"] < 1):
        raise ValueError("alpha/margin")
    if policy.get("optional_stopping") is not False or policy.get("stratum_claims") != "descriptive-only" or policy.get("estimand") != "fixed-panel-equal-task-mean":
        raise ValueError("unsupported inferential policy")
    if not policy.get("exposure_required_scope") or policy.get("launch_authorized") is not False:
        raise ValueError("explicit exposure scope/offline policy required")
    if policy.get("methods_module_sha256") != module_hash():
        raise ValueError("exact mechanism source hash changed")


def _unique_ids(rows):
    ids = [x["id"] for x in rows]
    if not all(isinstance(x, str) and x for x in ids) or len(ids) != len(set(ids)):
        raise ValueError("invalid or duplicate IDs")


def _span(span):
    if set(span) != {"repo", "path", "start", "end"}:
        raise ValueError("material span schema")
    if not all(isinstance(span[x], str) and span[x] for x in ("repo", "path")):
        raise ValueError("material identity")
    path = span["path"]
    if path.startswith("/") or "\\" in path or any(x in ("", ".", "..") for x in path.split("/")):
        raise ValueError("noncanonical material path")
    if type(span["start"]) is not int or type(span["end"]) is not int or not 1 <= span["start"] <= span["end"]:
        raise ValueError("inclusive line bounds")


def _overlap(a, b):
    return any(x["repo"] == y["repo"] and x["path"] == y["path"] and
               max(x["start"], y["start"]) <= min(x["end"], y["end"])
               for x in a["material"] for y in b["material"])


def validate_frame(candidates, exposure, policy):
    validate_policy(policy)
    _unique_ids(candidates)
    if exposure.get("schema") != "exposure-registry-v1" or exposure.get("independent_completeness_review") != "PASS":
        raise ValueError("independently complete exposure registry required")
    if set(exposure.get("required_scope", [])) != set(policy["exposure_required_scope"]):
        raise ValueError("exposure scope incomplete")
    if not exposure.get("source_manifest_hashes"):
        raise ValueError("exposure source hashes required")
    for key in ("task_ids", "obligation_ids", "task_fingerprints"):
        if not isinstance(exposure.get(key), list) or not all(isinstance(x, str) for x in exposure[key]):
            raise ValueError("exposure lists required")
    for row in candidates:
        if row.get("stratum") not in policy["strata"] or not isinstance(row.get("cell"), str) or not row["cell"]:
            raise ValueError("candidate stratum/cell")
        if (not isinstance(row.get("obligations"), list) or not row["obligations"] or
            not all(isinstance(x, str) and x for x in row["obligations"]) or len(set(row["obligations"])) != len(row["obligations"]) or
            not isinstance(row.get("fingerprint"), str) or not row["fingerprint"] or not row.get("material")):
            raise ValueError("candidate identity/material/obligations required")
        pin_hash = row.get("source_pin_manifest_hash")
        if not isinstance(pin_hash, str) or len(pin_hash) != 64 or any(x not in "0123456789abcdef" for x in pin_hash):
            raise ValueError("candidate source pin manifest hash required")
        if any("\0" in row[key] for key in ("id", "cell")):
            raise ValueError("NUL in sampling identifiers")
        if row.get("access_source_review") != "PASS" or row.get("request_gold_alignment") != "PASS":
            raise ValueError("candidate independent reviews required")
        for span in row["material"]:
            _span(span)
    for span in exposure.get("source_spans", []):
        _span(span)
    for path in exposure.get("touched_paths", []):
        _span({"repo": path["repo"], "path": path["path"], "start": 1, "end": 1})


def lock(candidates, exposure, seed_commitment, policy):
    validate_frame(candidates, exposure, policy)
    if not isinstance(seed_commitment, str) or len(seed_commitment) != 64 or any(x not in "0123456789abcdef" for x in seed_commitment):
        raise ValueError("seed SHA-256 commitment required")
    body = {"schema": "preselection-lock-v1", "protocol_hash": digest(policy),
            "candidate_hash": digest(candidates), "exposure_hash": digest(exposure), "seed_commitment": seed_commitment}
    return {**body, "lock_hash": digest(body)}


def quotas(counts, slots):
    total = sum(counts.values())
    if total < slots:
        raise ValueError("insufficient eligible frame")
    result = {key: slots * count // total for key, count in counts.items()}
    order = sorted(counts, key=lambda key: (-(slots * counts[key] % total), key))
    for key in order[:slots - sum(result.values())]:
        result[key] += 1
    return result


def sample(candidates, exposure, seed, frozen_lock, policy):
    if not isinstance(seed, str) or not seed or "\0" in seed:
        raise ValueError("nonempty canonical seed required")
    expected = lock(candidates, exposure, hashlib.sha256(seed.encode()).hexdigest(), policy)
    if expected != frozen_lock:
        raise ValueError("preselection lock or input tampered")
    eligible, excluded = [], []
    for row in candidates:
        reasons = []
        if row["id"] in exposure["task_ids"] or row["fingerprint"] in exposure["task_fingerprints"]:
            reasons.append("exposed_task")
        if set(row["obligations"]) & set(exposure["obligation_ids"]):
            reasons.append("exposed_obligation")
        if _overlap(row, {"material": exposure.get("source_spans", [])}):
            reasons.append("exposed_source_span")
        if any(s["repo"] == t["repo"] and (s["path"] == t["path"] or s["path"].startswith(t["path"] + "/"))
               for s in row["material"] for t in exposure.get("touched_paths", [])):
            reasons.append("touched_path")
        (excluded if reasons else eligible).append({"id": row["id"], "reasons": reasons} if reasons else row)
    selected, skipped, allocation = [], [], {}
    for stratum in policy["strata"]:
        groups = {}
        for row in eligible:
            if row["stratum"] == stratum:
                groups.setdefault(row["cell"], []).append(row)
        allocation[stratum] = quotas({key: len(rows) for key, rows in groups.items()}, policy["tasks_per_stratum"])
        for cell in sorted(groups):
            rows = sorted(groups[cell], key=lambda x: (hashlib.sha256((seed + "\0" + stratum + "\0" + cell + "\0" + x["id"]).encode()).hexdigest(), x["id"]))
            taken = 0
            for row in rows:
                if taken == allocation[stratum][cell]:
                    break
                if any(row["fingerprint"] == old["fingerprint"] or set(row["obligations"]) & set(old["obligations"]) or _overlap(row, old) for old in selected):
                    skipped.append({"id": row["id"], "reason": "selected_material_obligation_collision"})
                    continue
                selected.append(row)
                taken += 1
            if taken != allocation[stratum][cell]:
                raise ValueError("collision prevents frozen cell quota; no redistribution")
    return {"schema": "selected-panel-v1", "lock": frozen_lock, "protocol_hash": digest(policy),
            "seed": seed, "selected": selected, "selected_hash": digest(selected), "excluded": excluded,
            "collision_skips": skipped, "quotas": allocation, "launch_authorized": False}


def schedule(panel, policy):
    validate_policy(policy)
    rows = panel["selected"]
    _unique_ids(rows)
    if (len(rows) != policy["tasks"] or any(sum(x["stratum"] == s for x in rows) != policy["tasks_per_stratum"] for s in policy["strata"]) or
        panel["selected_hash"] != digest(rows) or panel.get("protocol_hash") != digest(policy)):
        raise ValueError("invalid exact panel")
    if hashlib.sha256(panel["seed"].encode()).hexdigest() != panel["lock"]["seed_commitment"]:
        raise ValueError("schedule seed changed")
    ordered = sorted(rows, key=lambda x: (hashlib.sha256((panel["seed"] + "\0schedule\0" + x["id"]).encode()).hexdigest(), x["id"]))
    result = []
    for i, task in enumerate(ordered):
        for j, repeat in enumerate(policy["repeats"]):
            arms = ("A", "B") if (i + j) % 2 == 0 else ("B", "A")
            for arm in arms:
                result.append({"id": f"{task['id']}:{repeat}:{arm}", "task": task["id"], "repeat": repeat, "arm": arm})
    return result


def _outcome(row):
    for key in ("semantic", "integrity", "ledger", "closure"):
        if row.get(key) not in ("pass", "fail", "unknown"):
            raise ValueError("typed assessment required")
    reason = row.get("confirmed_failure")
    if reason not in (None, "source_unavailable", "budget_exhausted", "timeout", "model_tool_failure", "malformed_or_no_answer"):
        raise ValueError("unrecognized terminal failure")
    if reason is not None or "fail" in (row["semantic"], row["integrity"], row["ledger"], row["closure"]):
        return (0.0, 0.0)
    if any(row[key] != "pass" for key in ("semantic", "integrity", "ledger", "closure")):
        return (0.0, 1.0)
    return (1.0, 1.0)


def analyze(panel, frozen_panel_hash, frozen_schedule_hash, results, policy, shared_fault=False):
    slots = schedule(panel, policy)
    if panel.get("protocol_hash") != digest(policy) or frozen_schedule_hash != digest(slots) or frozen_panel_hash != digest(panel):
        raise ValueError("protocol/schedule binding changed")
    _unique_ids(results)
    if set(x["id"] for x in slots) != set(x["id"] for x in results):
        raise ValueError("exact frozen-slot roster required; include missing/unlaunched")
    if type(shared_fault) is not bool:
        raise ValueError("typed shared fault required")
    by_id = {x["id"]: x for x in results}
    counts = {k: 0 for k in ("terminal", "missing", "unlaunched")}
    bounds = {}
    for slot in slots:
        row = by_id[slot["id"]]
        if any(row.get(key) != slot[key] for key in ("task", "repeat", "arm")):
            raise ValueError("slot identity changed")
        status = row.get("status")
        if status not in counts:
            raise ValueError("typed admission status required")
        counts[status] += 1
        if status == "terminal":
            bounds[slot["id"]] = _outcome(row)
    if shared_fault or counts["missing"] or counts["unlaunched"]:
        return {"decision": "blocked", "reasons": [key for key, value in (("shared_method_fault", shared_fault), ("incomplete_schedule", counts["missing"] or counts["unlaunched"])) if value],
                "accounting": counts, "original_denominator": policy["attempts"], "confirmatory_interval": None}
    lower, upper = [], []
    for task in panel["selected"]:
        low, high = 0.0, 0.0
        for r in policy["repeats"]:
            a = bounds[f"{task['id']}:{r}:A"]
            b = bounds[f"{task['id']}:{r}:B"]
            low += (a[0] - b[1]) / len(policy["repeats"])
            high += (a[1] - b[0]) / len(policy["repeats"])
        lower.append(low)
        upper.append(high)
    eps = math.sqrt(2 * math.log(2 / policy["alpha"]) / policy["tasks"])
    lo, hi = max(-1.0, sum(lower) / policy["tasks"] - eps), min(1.0, sum(upper) / policy["tasks"] + eps)
    decision = "A_practically_superior" if lo > policy["margin"] else "B_practically_superior" if hi < -policy["margin"] else "no_practical_superiority_established"
    return {"decision": decision, "accounting": counts, "original_denominator": policy["attempts"],
            "task_units": policy["tasks"], "task_mean_identification_interval": [sum(lower) / policy["tasks"], sum(upper) / policy["tasks"]],
            "confirmatory_interval": [lo, hi], "epsilon": eps, "margin": policy["margin"],
            "unknown_attempts": sum(lo != hi for lo, hi in bounds.values()), "stratum_claims": "descriptive-only",
            "launch_authorized": False}
