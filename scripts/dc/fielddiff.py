#!/usr/bin/env python3
"""Report every field a constructed cassette claims that no recording carries.

Run from the repository root, after a recording session:

    python3 scripts/dc/fielddiff.py

A hand-written fixture asserts both halves of an exchange, so it can invent a
field the server never sends — and the code then comes to depend on it. That is
not hypothetical: it is how `has-screen`, a board's `project`, a project's
`private` and a component's `lead` all came to be published and to be
structurally empty on every Data Center, and how `issue attachment download`
came to require an `id` a real instance omits.

**This is an operator tool and not a gate, on purpose.** Most of what it
reports is ordinary data dependence: an issue with no links has no
`issuelinks`, a sprint with no dates has no `startDate`, a version with no
release date has no `releaseDate`. Telling those from an invented field is
judgement — does the server send this whenever the parent object exists, or
only when there is a value? — and a check that cannot make the distinction
would either cry wolf or have to allowlist most of its own output.

What to do with a hit: probe the live rig for the field under every expand the
endpoint documents, the way scripts/dc/README.md describes. If nothing produces
it, the fixture invented it.
"""
import json
import os
import re
from collections import defaultdict

ROOT = "internal"

def norm(path):
    p = re.sub(r'/[A-Z][A-Z0-9]*-\d+', '/{key}', path)
    p = re.sub(r'/\d+', '/{id}', p)
    return p

def paths(obj, prefix=""):
    """Every dotted field path in a JSON document, arrays flattened."""
    out = set()
    if isinstance(obj, dict):
        for k, v in obj.items():
            here = f"{prefix}.{k}" if prefix else k
            out.add(here)
            out |= paths(v, here)
    elif isinstance(obj, list):
        for item in obj[:5]:
            out |= paths(item, prefix + "[]")
    return out

recorded = defaultdict(set)
constructed = defaultdict(lambda: defaultdict(set))  # group -> field -> {files}

for dirpath, _, files in os.walk(ROOT):
    for name in files:
        if not name.endswith(".datacenter.json"):
            continue
        full = os.path.join(dirpath, name)
        try:
            c = json.load(open(full))
        except Exception:
            continue
        if "interactions" not in c:
            continue
        src = c.get("source", "constructed")
        for i in c["interactions"]:
            req = i["request"]
            key = f'{req.get("method","GET")} {norm(req["path"])}'
            body = i["response"].get("body")
            if not body:
                continue
            try:
                doc = json.loads(body)
            except Exception:
                continue
            fields = paths(doc)
            if src == "recorded":
                recorded[key] |= fields
            else:
                for f in fields:
                    constructed[key][f].add(full)

print(f"{len(recorded)} recorded endpoint shapes, {len(constructed)} constructed\n")
hits = 0
for key in sorted(constructed):
    if key not in recorded:
        continue
    invented = {f: files for f, files in constructed[key].items() if f not in recorded[key]}
    if not invented:
        continue
    hits += 1
    print(f"=== {key}")
    for f in sorted(invented):
        files = ", ".join(sorted(os.path.basename(x) for x in invented[f]))
        print(f"    {f:<52} {files}")
print(f"\n{hits} endpoint shapes where a constructed cassette asserts a field no recording carries")
