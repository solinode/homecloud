#!/usr/bin/env python3
"""Turns the runner's TSV into compat/results.json and compat/results.md.

usage: report.py <results.tsv> <out-dir> <tool version>

Results of scenarios not in this run are kept from the previous results.json, so running a
subset updates just those rows. Exits 1 when a scenario in this run failed a step.
"""
import datetime
import glob
import json
import os
import re
import sys

tsv, out, tool = sys.argv[1], sys.argv[2], sys.argv[3]
suite = os.path.join(os.path.dirname(os.path.abspath(__file__)), "terraform")


def meta(name):
    """Modules (source@version) and services a scenario declares in its .tf files."""
    modules, services = [], []
    for f in sorted(glob.glob(os.path.join(suite, name, "*.tf"))):
        text = open(f).read()
        for m in re.finditer(r'source\s*=\s*"(terraform-aws-modules/[^"]+)"\s*\n\s*version\s*=\s*"([^"]+)"', text):
            mod = f"{m.group(1).removeprefix('terraform-aws-modules/')}@{m.group(2)}"
            if mod not in modules:
                modules.append(mod)
        for m in re.finditer(r"^# services:\s*(.+)$", text, re.M):
            services += [s.strip() for s in m.group(1).split(",") if s.strip() not in services]
    return modules, services


path = os.path.join(out, "results.json")
try:
    prev = {r["scenario"]: r for r in json.load(open(path))["scenarios"]}
except (OSError, ValueError, KeyError):
    prev = {}

failed = False
for line in open(tsv):
    name, apply, check, idem, destroy, secs, notes = (line.rstrip("\n").split("\t") + [""] * 7)[:7]
    modules, services = meta(name)
    ok = apply == "pass" and check in ("pass", "skip") and idem == "pass" and destroy == "pass"
    failed |= not ok
    prev[name] = {
        "scenario": name, "modules": modules, "services": services, "apply": apply, "check": check,
        "idempotent": idem, "destroy": destroy, "pass": ok, "seconds": int(secs or 0), "notes": notes,
    }

rows = [prev[k] for k in sorted(prev)]
doc = {
    "generated": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "tool": tool,
    "passed": sum(r["pass"] for r in rows),
    "total": len(rows),
    "scenarios": rows,
}
with open(path, "w") as f:
    json.dump(doc, f, indent=2)
    f.write("\n")

mark = {"pass": "pass", "fail": "**FAIL**", "skip": "-"}
lines = [
    f"{doc['passed']} of {doc['total']} scenarios pass ({tool}, {doc['generated']}).",
    "",
    "| Scenario | Modules | Apply | Check | Idempotent | Destroy | Notes |",
    "|---|---|---|---|---|---|---|",
]
for r in rows:
    lines.append("| {} | {} | {} | {} | {} | {} | {} |".format(
        r["scenario"], "<br>".join(r["modules"]), mark.get(r["apply"], r["apply"]), mark.get(r["check"], r["check"]),
        mark.get(r["idempotent"], r["idempotent"]), mark.get(r["destroy"], r["destroy"]),
        r["notes"].replace("|", "\\|")))

score = {}
for r in rows:
    for s in r["services"]:
        p, t = score.get(s, (0, 0))
        score[s] = (p + r["pass"], t + 1)
lines += ["", "| Service | Scenarios passing |", "|---|---|"]
for s in sorted(score):
    lines.append(f"| {s} | {score[s][0]}/{score[s][1]} |")
with open(os.path.join(out, "results.md"), "w") as f:
    f.write("\n".join(lines) + "\n")
print("\n".join(lines))
sys.exit(1 if failed else 0)
