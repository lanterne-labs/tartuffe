import os, sys, psycopg
# Read-write SQL on prod: stdin is split on ";;", every statement runs in one
# transaction, committed at the end. Only for a cleanup the user approved.
# Usage: printf "%s" "UPDATE ..." | DBURL=... uv run -q --with "psycopg[binary]" python -I qw.py
qs = sys.stdin.read().split(';;')
with psycopg.connect(os.environ["DBURL"]) as c:
    for q in qs:
        q = q.strip()
        if not q:
            continue
        print("##", q[:100])
        cur = c.execute(q)
        if cur.description:
            print(" | ".join(d.name for d in cur.description))
            for r in cur.fetchall():
                print(" | ".join(str(x) for x in r))
        else:
            print("rows:", cur.rowcount)
    c.commit()
