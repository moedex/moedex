import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("compare", Path(__file__).with_name("compare.py"))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class CompareTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.serial = 0
        self.contract = {
            "schema": "native-pair-v1",
            "corpus": {"repository": "https://example.test/repo", "commit": "a" * 40,
                       "manifest": self.ref({"files": []})},
            "rubric": self.ref({"reviewed": True}), "protocol": self.ref({"native": True}),
            "tasks": [{"id": "t1", "prompt": self.ref("Locate the registration."), "atoms": ["a1", "a2"]}],
            "budgets": {"calls": 24, "response_bytes": 131072, "assignment_seconds": 600, "display_bytes": 8192}}
        self.hash = c.digest(c.canonical(self.contract))
        self.arms = [self.arm("left"), self.arm("right")]

    def ref(self, value):
        self.serial += 1
        data = value.encode() if isinstance(value, str) else json.dumps(value).encode()
        path = str(self.serial) + ".json"
        (self.root / path).write_bytes(data)
        return {"path": path, "sha256": c.digest(data)}

    def arm(self, name):
        freeze = self.ref({"binary": name})
        setup = self.ref({"ready": True})
        answer = self.ref("Cited answer")
        return {"name": name, "native": True, "contract_sha256": self.hash,
                "freeze": freeze, "setup": setup, "tasks": [{"id": "t1", "status": "answered",
                    "solver_id": name + "-solver", "setup": setup, "answer": answer,
                    "accounting": self.ref({"task": "t1", "contract_sha256": self.hash,
                        "freeze_sha256": freeze["sha256"], "prompt_sha256": self.contract["tasks"][0]["prompt"]["sha256"],
                        "calls": 2, "response_bytes_observed": 100, "assignment_seconds": 10,
                        "max_display_bytes": 100, "transport_complete": True, "transcript_verified": True,
                        "onboarding_complete": True}),
                    "review": self.ref({"task": "t1", "answer_sha256": answer["sha256"],
                        "rubric_sha256": self.contract["rubric"]["sha256"], "reviewer_id": name + "-reviewer",
                        "adjudication_final": True, "material_unsupported_claims": 0,
                        "runtime_distinction_preserved": True,
                        "atoms": [{"id": a, "correctness": 1, "evidence": 1} for a in ("a1", "a2")]})}]}

    def edit(self, key, update):
        task = self.arms[0]["tasks"][0]
        value = json.loads((self.root / task[key]["path"]).read_text())
        update(value)
        task[key] = self.ref(value)

    def run_compare(self):
        return c.compare(self.contract, self.arms, self.root)

    def test_complete_and_deterministic(self):
        a = self.run_compare()
        self.assertEqual(a, self.run_compare())
        self.assertEqual(a["classification"], "complete paired coverage")
        self.assertEqual(a["paired_task_count"], 1)
        self.assertEqual(a["arms"][0]["strict_successes"], 1)
        self.assertEqual(a["paired_tasks"][0]["correctness_delta"], 0)

    def test_missing_arm_setup_does_not_score(self):
        self.arms[1]["setup"] = self.ref({"ready": False})
        self.arms[1]["tasks"] = [{"id": "t1", "status": "setup_failed", "reason": "service unavailable",
                                  "setup": self.arms[1]["setup"]}]
        result = self.run_compare()
        self.assertEqual(result["paired_tasks"], [])
        self.assertEqual(result["arms"][1]["assigned_tasks"], 0)
        self.assertEqual(result["arms"][1]["eligible_atoms"], 0)

    def test_unassigned_preserves_planned_denominator(self):
        self.arms[0]["tasks"] = [{"id": "t1", "status": "unassigned", "reason": "not launched"}]
        r = self.run_compare()
        self.assertEqual(r["planned_task_count"], 1)
        self.assertEqual(r["arms"][0]["assigned_tasks"], 0)
        self.assertIsNone(r["arms"][0]["tasks"][0]["correctness"])

    def test_blocked_unknown_bytes(self):
        t = self.arms[0]["tasks"][0]
        t.update(status="blocked", reason="transport interrupted")
        del t["answer"], t["review"]
        self.edit("accounting", lambda a: a.update(transport_complete=False, response_bytes_observed=0))
        r = self.run_compare()
        self.assertEqual(r["arms"][0]["assigned_tasks"], 1)
        self.assertFalse(r["arms"][0]["assigned_transport_complete"])
        self.assertEqual(r["paired_tasks"], [])

    def test_budgets_and_audit_exclude_even_correct_answers(self):
        original = copy.deepcopy(self.arms)
        for update in ({"calls": 25}, {"response_bytes_observed": 131073}, {"assignment_seconds": 601},
                       {"max_display_bytes": 8193}, {"transport_complete": False},
                       {"transcript_verified": False}, {"onboarding_complete": False}):
            with self.subTest(update=update):
                self.arms = copy.deepcopy(original)
                self.edit("accounting", lambda a: a.update(update))
                r = self.run_compare()
                self.assertEqual(r["paired_task_count"], 0)
                self.assertEqual(r["arms"][0]["strict_successes"], 0)
                self.assertEqual(r["arms"][0]["tasks"][0]["correctness"], 2)

    def test_semantic_errors_remain_comparable(self):
        self.edit("review", lambda r: r.update(material_unsupported_claims=1))
        r = self.run_compare()
        self.assertEqual(r["paired_task_count"], 1)
        self.assertEqual(r["arms"][0]["strict_successes"], 0)
        self.assertEqual(r["paired_tasks"][0]["strict_success_delta"], -1)

    def test_reject_mismatched_bindings_and_invalid_scores(self):
        original = copy.deepcopy(self.arms)
        cases = [("accounting", {"freeze_sha256": "wrong"}), ("accounting", {"prompt_sha256": "wrong"}),
                 ("accounting", {"contract_sha256": "wrong"}), ("accounting", {"task": "other"}),
                 ("accounting", {"calls": True}), ("accounting", {"assignment_seconds": float("nan")}),
                 ("review", {"answer_sha256": "wrong"}), ("review", {"rubric_sha256": "wrong"}),
                 ("review", {"adjudication_final": False}), ("review", {"reviewer_id": "left-solver"}),
                 ("review", {"atoms": [{"id": "a1", "correctness": 2, "evidence": 1},
                                        {"id": "a2", "correctness": 1, "evidence": 1}]}),
                 ("review", {"atoms": []})]
        for key, update in cases:
            with self.subTest(key=key, update=update):
                self.arms = copy.deepcopy(original)
                self.edit(key, lambda a: a.update(update))
                with self.assertRaises(c.Invalid):
                    self.run_compare()

    def test_reject_missing_tasks_setup_substitution_and_non_native(self):
        original = copy.deepcopy(self.arms)
        for update in ({"tasks": []}, {"contract_sha256": "wrong"}, {"native": False},
                       {"setup": self.ref({"ready": False})}):
            self.arms = copy.deepcopy(original)
            self.arms[0].update(update)
            with self.assertRaises(c.Invalid):
                self.run_compare()

    def test_reject_duplicate_tasks_atoms_and_unpinned_source(self):
        original = copy.deepcopy(self.arms)
        self.arms[0]["tasks"].append(copy.deepcopy(self.arms[0]["tasks"][0]))
        with self.assertRaises(c.Invalid):
            self.run_compare()
        self.arms = original
        self.edit("review", lambda r: r["atoms"].append(copy.deepcopy(r["atoms"][0])))
        with self.assertRaises(c.Invalid):
            self.run_compare()
        self.contract["corpus"]["commit"] = "main"
        with self.assertRaises(c.Invalid):
            self.run_compare()

    def test_evidence_tamper_and_escape(self):
        ref = self.arms[0]["freeze"]
        (self.root / ref["path"]).write_text("tampered")
        with self.assertRaises(c.Invalid):
            self.run_compare()
        with self.assertRaises(c.Invalid):
            c.Evidence(self.root).read({"path": "../outside", "sha256": "x"})

    def test_cli_rejects_overwrite(self):
        contract = self.root / "contract.json"
        contract.write_text(json.dumps(self.contract))
        paths = []
        for i, arm in enumerate(self.arms):
            p = self.root / ("arm" + str(i) + ".json")
            p.write_text(json.dumps(arm))
            paths += ["--arm", str(p)]
        out = self.root / "report.json"
        cmd = [sys.executable, str(Path(c.__file__)), "--root", str(self.root),
               "--contract", str(contract), *paths, "--output", str(out)]
        self.assertEqual(subprocess.run(cmd, capture_output=True).returncode, 0)
        before = out.read_bytes()
        self.assertEqual(subprocess.run(cmd, capture_output=True).returncode, 2)
        self.assertEqual(out.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
