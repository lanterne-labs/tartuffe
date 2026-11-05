"""Record one audit's results for the Grafana dashboard Quality: per sample and
scope (new window, history band), how many rows were judged and how many were
wrong, as rows of job_metrics under the job `audit_run`, dated at the run.

Reads <dir>/*.audit.jsonl and <dir>/*.verdicts.jsonl. A row's scope is its
tag/from/src/origin field; its result is the first of verdict(s), type(s),
problem(s). Dry run by default; --apply replaces the rows of that run date.

  DATABASE_URL=... python -I audit_record.py --dir audit-1105 --at 2026-11-05T15:00:00Z [--apply]
"""
import argparse
import collections
import glob
import json
import os
import re
import sys

# Not errors: fine rows, and findings the audit counts apart (a grouped
# duplicate is not an error of the data; old_summary is the non-candidates'
# summaries, mirror-08m; sd_missing, a date the judges read from the source).
OK = {"ok", "clean", "clean_new", "ok_split_summary", "duplicate_grouped",
      "old_summary", "sd_missing", "none", ""}
SAMPLES = {"text": "text", "text_band": "text", "text_window": "text",
           "votes": "votes", "debates": "debates", "stances": "stances",
           "entities": "entities"}


def scope_of(row):
    for k in ("tag", "from", "src", "origin"):
        v = str(row.get(k) or "").lower()
        if v.startswith(("band", "hist", "old")):
            return "band"
        if v.startswith(("window", "new", "recent")):
            return "window"
    return "all"


def problems_of(row):
    """The row's error types; empty when the row is fine."""
    for k in ("types", "problems", "verdicts", "type", "problem", "verdict"):
        if k not in row:
            continue
        v = row[k]
        if isinstance(v, dict):
            v = [t for t, on in v.items() if on]
        items = v if isinstance(v, list) else re.split(r"[,;]", str(v))
        norm = [str(i).strip().lower().replace(" ", "_") for i in items]
        return [t for t in norm if t and t not in OK]
    return []


def read(path):
    with open(path) as f:
        return [json.loads(l) for l in f if l.strip()]


def summarise(folder):
    out = collections.defaultdict(lambda: collections.Counter())
    for path in sorted(glob.glob(os.path.join(folder, "*.audit.jsonl"))):
        sample = SAMPLES.get(os.path.basename(path).split(".")[0])
        if sample is None:
            continue
        for row in read(path):
            c = out[f"{sample}/{scope_of(row)}"]
            found = problems_of(row)
            c["n"] += 1
            c["errors"] += 1 if found else 0
            for t in found:
                c["type:" + t] += 1
    # Contradictions: one verdict per id, the file `all` when there is one.
    files = glob.glob(os.path.join(folder, "all.verdicts.jsonl")) or \
        glob.glob(os.path.join(folder, "*.verdicts.jsonl"))
    seen = {}
    for path in files:
        for row in read(path):
            seen[row["id"]] = row["verdict"]
    if seen:
        c = out["contradictions/all"]
        c["n"] = len(seen)
        c["errors"] = sum(v == "false" for v in seen.values())
        c["debatable"] = sum(v == "debatable" for v in seen.values())
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--at", required=True, help="the run's time, ISO 8601")
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()
    data = summarise(args.dir)
    for target in sorted(data):
        c = data[target]
        kinds = {k[5:]: v for k, v in c.items() if k.startswith("type:")}
        print(f"{target:24} n={c['n']:4} errors={c['errors']:4} "
              f"{100 * c['errors'] / c['n']:5.1f} %  {kinds or ''}")
    if not args.apply:
        print("(dry run)", file=sys.stderr)
        return
    import psycopg
    with psycopg.connect(os.environ["DATABASE_URL"]) as conn:
        conn.execute("DELETE FROM job_metrics WHERE job_type='audit_run' AND created_at=%s", (args.at,))
        for target, c in data.items():
            for metric, value in c.items():
                conn.execute(
                    "INSERT INTO job_metrics (job_type, target, metric, value, created_at) "
                    "VALUES ('audit_run', %s, %s, %s, %s)",
                    (target, metric, value, args.at))
    print(f"recorded {len(data)} samples at {args.at}")


if __name__ == "__main__":
    main()
