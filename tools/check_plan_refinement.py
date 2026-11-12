#!/usr/bin/env python3
"""Check workspace planning resources and record structure, not reasoning quality."""

import argparse
import hashlib
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
PINNED = {
    "workflow/ponytail/SKILL.md": "1316a2f3f95741d2300b116fe0c2d81ce4a9568656ed0a62643f54aaf09957f2",
    "workflow/ponytail/LICENSE": "fb1bc6909ac3ef82d5c22106e32ef682b0cff66788fa915fb9b53b15c9d2f3ab",
}
FIELDS = (
    "Objective", "System fit", "Reuse and simplicity", "Evidence and unknowns",
    "Verification", "Resource budget",
)
WORKFLOW_DOCS = (
    "AGENTS.md", "docs/PLAN_REFINEMENT.md", "workflow/ponytail/README.md",
    "tasks/todo.md", "tasks/lessons.md",
)


def check(plan: Path) -> list[str]:
    errors = []
    for name in WORKFLOW_DOCS:
        document = ROOT / name
        if not document.is_file():
            errors.append(f"Missing workflow resource: {name}")
            continue
        for target in re.findall(r"\[[^\]]*\]\(([^\s)]+)\)", document.read_text()):
            if re.match(r"[a-zA-Z][a-zA-Z0-9+.-]*:", target):
                continue
            target = target.split("#", 1)[0]
            if target and not (document.parent / target).exists():
                errors.append(f"Broken local workflow link in {name}: {target}")
    for name, expected in PINNED.items():
        path = ROOT / name
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            errors.append(f"Missing or changed pinned resource: {name}")
    if not (ROOT / "workflow/ponytail/README.md").is_file():
        errors.append("Missing Ponytail provenance: workflow/ponytail/README.md")
    instructions = ROOT / "AGENTS.md"
    if not instructions.is_file():
        errors.append("Missing root AGENTS.md")
    else:
        text = instructions.read_text()
        for resource in ("workflow/ponytail/SKILL.md", "docs/PLAN_REFINEMENT.md"):
            if resource not in text or not (ROOT / resource).is_file():
                errors.append(f"Root instructions must link an existing {resource}")
    if not plan.is_file():
        return errors + [f"Plan does not exist: {plan}"]
    text = plan.read_text()
    records = list(re.finditer(r"^## Plan refinement\s*$", text, re.MULTILINE))
    if not records:
        return errors + ["Add a '## Plan refinement' section with the six required fields"]
    section = re.split(r"^#{1,2} ", text[records[-1].end():], maxsplit=1, flags=re.MULTILINE)[0]
    for field in FIELDS:
        match = re.search(r"^- \*\*" + re.escape(field) + r":\*\*[ \t]*([^\n]*)", section, re.MULTILINE)
        value = match.group(1).strip() if match else ""
        if not value or re.fullmatch(r"(?i)(tbd|todo|n/a|none|\.\.\.|\[.*\])\.?", value):
            errors.append(f"Write a concrete '{field}' decision in the latest refinement record")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("plan", type=Path)
    errors = check(parser.parse_args().plan)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("Plan resources and refinement structure pass; semantic review is still required.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
