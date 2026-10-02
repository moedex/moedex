#!/usr/bin/env python3
"""Validate frozen paired native-arm records; report coverage before score deltas.

No network, product execution, scoring, or inferred setup success. See README.
"""
import argparse
import hashlib
import json
import math
import re
from pathlib import Path


class Invalid(ValueError):
    pass


def require(ok, message):
    if not ok:
        raise Invalid(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def number(value, name, integer=False):
    require(type(value) in (int, float) and math.isfinite(value) and value >= 0,
            name + " must be a finite nonnegative number")
    require(not integer or type(value) is int, name + " must be an integer")
    return value


def unique(rows, key, label):
    require(isinstance(rows, list), label + " must be a list")
    result = {}
    for row in rows:
        ident = row[key]
        require(isinstance(ident, str) and ident and ident not in result,
                label + " contains an empty or duplicate identity")
        result[ident] = row
    return result


class Evidence:
    def __init__(self, root):
        self.root = Path(root).resolve()

    def read(self, ref, as_json=True):
        require(set(ref) == {"path", "sha256"}, "invalid evidence reference")
        path = (self.root / ref["path"]).resolve()
        require(not Path(ref["path"]).is_absolute() and path.is_relative_to(self.root),
                "evidence path escapes root")
        data = path.read_bytes()
        require(digest(data) == ref["sha256"], "evidence hash mismatch: " + ref["path"])
        return json.loads(data) if as_json else data


def compare(contract, arms, root):
    ev = Evidence(root)
    require(contract["schema"] == "native-pair-v1", "unknown contract schema")
    require(len(arms) == 2, "exactly two arms required")
    require(isinstance(contract["corpus"]["commit"], str) and
            re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", contract["corpus"]["commit"]) and
            contract["corpus"]["repository"], "unpinned corpus")
    for ref in (contract["corpus"]["manifest"], contract["rubric"], contract["protocol"]):
        ev.read(ref, False)
    tasks = unique(contract["tasks"], "id", "contract tasks")
    require(tasks, "empty task roster")
    for task in tasks.values():
        ev.read(task["prompt"], False)
        require(task["atoms"] and len(set(task["atoms"])) == len(task["atoms"]), "invalid atom roster")
        require(all(isinstance(a, str) and a for a in task["atoms"]), "invalid atom identity")
    budget = contract["budgets"]
    for key in ("calls", "response_bytes", "assignment_seconds", "display_bytes"):
        require(number(budget[key], key, key != "assignment_seconds") > 0, "zero budget")
    contract_hash = digest(canonical(contract))
    names = set()
    reports = []
    for arm in arms:
        require(arm["name"] and arm["name"] not in names, "duplicate/empty arm name")
        names.add(arm["name"])
        require(arm["contract_sha256"] == contract_hash, "arm contract mismatch")
        require(arm["native"] is True, "non-native arm cannot enter native comparison")
        ev.read(arm["freeze"], False)
        setup = ev.read(arm["setup"])
        require(type(setup["ready"]) is bool, "invalid setup readiness")
        records = unique(arm["tasks"], "id", "arm tasks")
        require(set(records) == set(tasks), "arm must account for every planned task")
        rows = []
        for ident, task in tasks.items():
            record = records[ident]
            status = record["status"]
            require(status in ("unassigned", "setup_failed", "blocked", "answered"), "unknown task status")
            row = {"id": ident, "status": status, "atoms": len(task["atoms"]),
                   "assigned": status in ("blocked", "answered"), "eligible": False,
                   "strict_success": False, "correctness": None, "evidence": None}
            if not row["assigned"]:
                require(not any(k in record for k in ("answer", "review", "accounting", "solver_id")),
                        "unassigned task cannot carry scored execution")
                require(isinstance(record["reason"], str) and record["reason"], "missing nonassignment reason")
                if status == "setup_failed":
                    require(ev.read(record["setup"])["ready"] is False, "task setup did not fail")
                row["reason"] = record["reason"]
                rows.append(row)
                continue
            require(setup["ready"], "assignment after failed arm setup")
            require(ev.read(record["setup"])["ready"] is True, "assignment without task preflight")
            audit = ev.read(record["accounting"])
            require(audit["task"] == ident and audit["contract_sha256"] == contract_hash,
                    "accounting identity mismatch")
            require(audit["freeze_sha256"] == arm["freeze"]["sha256"], "product changed during arm")
            require(audit["prompt_sha256"] == task["prompt"]["sha256"], "task prompt mismatch")
            calls = number(audit["calls"], "calls", True)
            observed = number(audit["response_bytes_observed"], "observed bytes", True)
            elapsed = number(audit["assignment_seconds"], "assignment elapsed")
            displayed = number(audit["max_display_bytes"], "display bytes", True)
            for key in ("transport_complete", "transcript_verified", "onboarding_complete"):
                require(type(audit[key]) is bool, "invalid audit flag: " + key)
            reasons = []
            for ok, reason in ((audit["transport_complete"], "unknown total transport bytes"),
                               (audit["transcript_verified"], "transcript not verified"),
                               (audit["onboarding_complete"], "onboarding incomplete"),
                               (calls <= budget["calls"], "call budget exceeded"),
                               (observed <= budget["response_bytes"], "byte budget exceeded"),
                               (elapsed <= budget["assignment_seconds"], "assignment deadline exceeded"),
                               (displayed <= budget["display_bytes"], "display cap exceeded")):
                if not ok:
                    reasons.append(reason)
            row.update(calls=calls, response_bytes_observed=observed,
                       transport_complete=audit["transport_complete"], assignment_seconds=elapsed)
            if status == "blocked":
                require("review" not in record and "answer" not in record, "blocked task cannot carry scores")
                require(record["reason"], "missing blocked reason")
                reasons.append(record["reason"])
            else:
                answer = ev.read(record["answer"], False)
                review = ev.read(record["review"])
                require(review["task"] == ident and review["answer_sha256"] == digest(answer), "review answer mismatch")
                require(review["rubric_sha256"] == contract["rubric"]["sha256"], "review rubric mismatch")
                require(record["solver_id"] and review["reviewer_id"] and
                        record["solver_id"] != review["reviewer_id"], "reviewer must differ from solver")
                require(review["adjudication_final"] is True, "review pending adjudication")
                scored = unique(review["atoms"], "id", "review atoms")
                require(set(scored) == set(task["atoms"]), "review atom roster mismatch")
                for atom in scored.values():
                    for key in ("correctness", "evidence"):
                        number(atom[key], key)
                        require(atom[key] in (0, .5, 1), "invalid atom score")
                unsupported = number(review["material_unsupported_claims"], "unsupported claims", True)
                require(type(review["runtime_distinction_preserved"]) is bool, "invalid runtime distinction")
                row.update(correctness=sum(a["correctness"] for a in scored.values()),
                           evidence=sum(a["evidence"] for a in scored.values()),
                           material_unsupported_claims=unsupported)
                row["eligible"] = not reasons
                row["strict_success"] = (not reasons and not unsupported and review["runtime_distinction_preserved"]
                                         and row["correctness"] == row["evidence"] == row["atoms"])
            row["exclusions"] = reasons
            rows.append(row)
        eligible = [r for r in rows if r["eligible"]]
        assigned = [r for r in rows if r["assigned"]]
        reports.append({"name": arm["name"], "setup_ready": setup["ready"], "tasks": rows,
                        "planned_tasks": len(rows), "planned_atoms": sum(r["atoms"] for r in rows),
                        "assigned_tasks": len(assigned), "assigned_atoms": sum(r["atoms"] for r in assigned),
                        "eligible_tasks": len(eligible), "eligible_atoms": sum(r["atoms"] for r in eligible),
                        "conditional_correctness": sum(r["correctness"] for r in eligible),
                        "conditional_evidence": sum(r["evidence"] for r in eligible),
                        "strict_successes": sum(r["strict_success"] for r in rows),
                        "attempted_calls": sum(r.get("calls", 0) for r in rows),
                        "response_bytes_observed": sum(r.get("response_bytes_observed", 0) for r in rows),
                        "assigned_transport_complete": bool(assigned) and all(r["transport_complete"] for r in assigned)})
    paired = []
    for left, right in zip(reports[0]["tasks"], reports[1]["tasks"]):
        if left["eligible"] and right["eligible"]:
            paired.append({"id": left["id"], "atoms": left["atoms"],
                           **{key + "_delta": left[key] - right[key] for key in
                              ("correctness", "evidence", "calls", "response_bytes_observed", "assignment_seconds")},
                           "strict_success_delta": int(left["strict_success"]) - int(right["strict_success"])})
    return {"schema": "native-pair-report-v1", "contract_sha256": contract_hash,
            "classification": "complete paired coverage" if len(paired) == len(tasks) else "incomplete paired coverage",
            "arms": reports, "paired_tasks": paired, "paired_task_count": len(paired),
            "planned_task_count": len(tasks), "delta_direction": reports[0]["name"] + " minus " + reports[1]["name"],
            "limitations": ["Evidence hashes bind supplied records, not their truth; independent audits remain required.",
                            "No significance test or competitive-win inference; subset deltas are conditional.",
                            "Model tokens and OS isolation are not measured."]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--contract", required=True)
    parser.add_argument("--arm", action="append", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    try:
        report = compare(json.loads(Path(args.contract).read_text()),
                         [json.loads(Path(p).read_text()) for p in args.arm], args.root)
        # Exclusive creation prevents silently replacing an earlier comparison.
        with open(args.output, "x") as out:
            out.write(json.dumps(report, indent=2, sort_keys=True, allow_nan=False) + "\n")
    except (Invalid, ValueError, TypeError, KeyError, OSError) as exc:
        parser.exit(2, "comparison rejected: " + str(exc) + "\n")


if __name__ == "__main__":
    main()
