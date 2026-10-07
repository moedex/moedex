import copy
import hashlib
import unittest

import panel_methods as h

STRATA = ("types", "paths", "mapping", "edges")

def policy():
    return {"schema":"offline-panel-policy-v1", "strata":list(STRATA), "tasks_per_stratum":12, "tasks":48, "repeats":[1,2], "arms":["A","B"], "attempts":192, "margin":.10, "alpha":.05, "estimand":"fixed-panel-equal-task-mean", "optional_stopping":False, "stratum_claims":"descriptive-only", "launch_authorized":False, "exposure_required_scope":["previous_tasks","development_tasks","touched_paths"], "methods_module_sha256":h.module_hash()}


def fixture():
    candidates = []
    for s in STRATA:
        for i in range(24):
            ident = f"{s}-{i}"
            candidates.append({"id": ident, "stratum": s, "cell": "go:single" if i < 18 else "csharp:cross",
                "fingerprint": "question-" + ident, "obligations": ["obligation-" + ident],
                "material": [{"repo": "invented", "path": ident + ".go", "start": 1, "end": 8}],
                "source_pin_manifest_hash": "b" * 64,
                "access_source_review": "PASS", "request_gold_alignment": "PASS"})
    exposure = {"schema": "exposure-registry-v1", "independent_completeness_review": "PASS",
        "required_scope": ["previous_tasks", "development_tasks", "touched_paths"],
        "source_manifest_hashes": ["a" * 64], "task_ids": [], "task_fingerprints": [], "obligation_ids": [],
        "source_spans": [], "touched_paths": []}
    return candidates, exposure


def panel(c=None, e=None):
    if c is None:
        c, e = fixture()
    seed = "invented-synthetic-seed"
    return h.sample(c, e, seed, h.lock(c, e, hashlib.sha256(seed.encode()).hexdigest(), policy()), policy())


def results(p, a="pass", b="fail"):
    return [{**s, "status": "terminal", "semantic": a if s["arm"] == "A" else b,
             "ledger": "pass", "integrity": "pass", "closure": "pass", "confirmed_failure": None}
            for s in h.schedule(p, policy())]


def analyze(p, rows, fault=False):
    return h.analyze(p, h.digest(p), h.digest(h.schedule(p, policy())), rows, policy(), fault)


class MethodsTests(unittest.TestCase):
    def test_exact_quota_reproducible_and_cell_allocation(self):
        p = panel()
        self.assertEqual(p, panel())
        self.assertEqual(len(p["selected"]), 48)
        self.assertEqual(len(h.schedule(p, policy())), 192)
        for s in STRATA:
            self.assertEqual(p["quotas"][s], {"go:single": 9, "csharp:cross": 3})
        self.assertEqual(h.quotas({"b": 1, "a": 1, "c": 1}, 2), {"b": 1, "a": 1, "c": 0})

    def test_exposure_task_obligation_span_and_touched_path(self):
        c, e = fixture()
        e["task_ids"] = [c[0]["id"]]
        e["obligation_ids"] = c[1]["obligations"]
        e["source_spans"] = c[2]["material"]
        e["touched_paths"] = [{"repo": "invented", "path": c[3]["material"][0]["path"]}]
        p = panel(c, e)
        self.assertEqual({x["id"] for x in p["excluded"]}, {x["id"] for x in c[:4]})
        self.assertFalse({x["id"] for x in p["selected"]} & {x["id"] for x in c[:4]})

    def test_lock_tamper_seed_and_duplicate_rejected(self):
        c, e = fixture()
        seed = "seed"
        lock = h.lock(c, e, hashlib.sha256(seed.encode()).hexdigest(), policy())
        with self.assertRaises(ValueError):
            h.sample(c, e, "changed", lock, policy())
        c[0]["fingerprint"] += "x"
        with self.assertRaises(ValueError):
            h.sample(c, e, seed, lock, policy())
        c.append(copy.deepcopy(c[0]))
        with self.assertRaises(ValueError):
            panel(c, e)

    def test_collision_cannot_silently_redistribute(self):
        c, e = fixture()
        for x in c[:24]:
            x["material"] = [{"repo": "invented", "path": "collision.go", "start": 1, "end": 8}]
        with self.assertRaisesRegex(ValueError, "collision"):
            panel(c, e)

    def test_strong_both_directions_and_weak(self):
        p = panel()
        self.assertEqual(analyze(p, results(p))["decision"], "A_practically_superior")
        self.assertEqual(analyze(p, results(p, "fail", "pass"))["decision"], "B_practically_superior")
        self.assertEqual(analyze(p, results(p, "pass", "pass"))["decision"], "no_practical_superiority_established")

    def test_unknown_adversarial(self):
        p = panel()
        r = analyze(p, results(p, "unknown", "unknown"))
        self.assertEqual(r["task_mean_identification_interval"], [-1, 1])
        self.assertEqual(r["unknown_attempts"], 192)
        self.assertEqual(r["decision"], "no_practical_superiority_established")
        rows = results(p)
        rows[0]["integrity"] = "unknown"
        r = analyze(p, rows)
        self.assertEqual(r["unknown_attempts"], 1)

    def test_missing_unlaunched_and_shared_fault_block(self):
        p = panel()
        for status in ("missing", "unlaunched"):
            rows = results(p)
            rows[0] = {**h.schedule(p, policy())[0], "status": status}
            r = analyze(p, rows)
            self.assertEqual(r["decision"], "blocked")
            self.assertEqual(r["accounting"][status], 1)
            self.assertEqual(r["original_denominator"], 192)
        self.assertEqual(analyze(p, results(p), True)["decision"], "blocked")

    def test_missing_roster_duplicate_and_material_tamper_rejected(self):
        p = panel()
        rows = results(p)
        with self.assertRaises(ValueError):
            analyze(p, rows[:-1])
        with self.assertRaises(ValueError):
            analyze(p, rows + [rows[0]])
        original_hash = h.digest(p)
        p["selected"][0]["material"][0]["end"] += 1
        p["selected_hash"] = h.digest(p["selected"])
        with self.assertRaises(ValueError):
            h.analyze(p, original_hash, h.digest(h.schedule(p, policy())), rows, policy())

    def test_confirmed_integrity_failure_and_unknown(self):
        p = panel()
        rows = results(p, "pass", "pass")
        for x in rows:
            if x["arm"] == "A":
                x["integrity"] = "fail"
        self.assertEqual(analyze(p, rows)["task_mean_identification_interval"], [-1, -1])
        for x in rows:
            if x["arm"] == "A":
                x["integrity"] = "unknown"
        self.assertEqual(analyze(p, rows)["task_mean_identification_interval"], [-1, 0])

    def test_seeded_schedule_order_balanced_reversed(self):
        p = panel()
        slots = h.schedule(p, policy())
        first = [x for i, x in enumerate(slots) if i % 4 == 0]
        self.assertEqual(sum(x["arm"] == "A" for x in first), 24)
        for i in range(0, len(slots), 4):
            self.assertEqual([x["arm"] for x in slots[i:i+2]], list(reversed([x["arm"] for x in slots[i+2:i+4]])))
        self.assertNotEqual([x["task"] for x in first], [x["id"] for x in p["selected"]])
        p["seed"] += "tampered"
        with self.assertRaises(ValueError):
            h.schedule(p, policy())

    def test_terminal_failure_and_bad_types(self):
        p = panel()
        rows = results(p, "unknown", "unknown")
        for x in rows:
            x["confirmed_failure"] = "source_unavailable"
        self.assertEqual(analyze(p, rows)["task_mean_identification_interval"], [0, 0])
        rows[0]["semantic"] = True
        with self.assertRaises(ValueError):
            analyze(p, rows)
        with self.assertRaises(ValueError):
            analyze(p, results(p), "false")


if __name__ == "__main__":
    unittest.main()
