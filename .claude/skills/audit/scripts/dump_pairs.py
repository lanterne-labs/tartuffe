"""Enrich a review sample: for each contradiction id in the jsonl on stdin, add
the fact ids and every source (url, published_at, source_type, original_quote)
of each fact. Read-only. Usage:
  DATABASE_URL=... python -I dump_pairs.py < sample.jsonl > enriched.jsonl
"""
import json
import os
import sys

import psycopg

rows = [json.loads(l) for l in sys.stdin if l.strip()]
ids = [r["id"] for r in rows]
out = {}
with psycopg.connect(os.environ["DATABASE_URL"]) as c:
    c.read_only = True
    cur = c.execute(
        "SELECT c.id, c.fact_a_id, c.fact_b_id, fa.fact_text, fb.fact_text, "
        "c.bundle_stance_id, c.reason_en "
        "FROM contradictions c JOIN facts fa ON fa.id=c.fact_a_id "
        "JOIN facts fb ON fb.id=c.fact_b_id WHERE c.id = ANY(%s)",
        (ids,),
    )
    for cid, a, b, ta, tb, st, ren in cur.fetchall():
        out[cid] = {"fact_a_id": a, "fact_b_id": b, "stance_id": st}
    fids = sorted({v["fact_a_id"] for v in out.values()} | {v["fact_b_id"] for v in out.values()})
    cur = c.execute(
        "SELECT fact_id, source_url, published_at, source_type, original_quote "
        "FROM fact_sources WHERE fact_id = ANY(%s) ORDER BY fact_id, published_at",
        (fids,),
    )
    src = {}
    for fid, url, pub, typ, quote in cur.fetchall():
        src.setdefault(fid, []).append({
            "url": url,
            "published_at": pub.isoformat() if pub else None,
            "source_type": typ,
            "quote": quote,
        })
for r in rows:
    e = out.get(r["id"])
    if e is None:
        r["missing"] = True
        continue
    r.update(e)
    r["quotes_a"] = src.get(e["fact_a_id"], [])
    r["quotes_b"] = src.get(e["fact_b_id"], [])
    print(json.dumps(r, ensure_ascii=False))
