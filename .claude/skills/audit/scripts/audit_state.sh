#!/bin/sh
# What the audit carries from one run to the next (table audit_state): the
# cursor in the history ("cursor") and the numbers of the last run
# ("last-run"). In the database, so every machine reads the same.
# Usage: audit_state.sh get <key>            prints the value, nothing when unset
#        audit_state.sh set <key> <value>
# Needs DBURL (see Setup).
set -eu
: "${DBURL:?set DBURL first (Setup)}"
case "${1:-}" in
  get)
    psql "$DBURL" -X -At -v key="$2" <<'SQL'
SELECT value || '  [saved ' || to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI') || 'Z]' FROM audit_state WHERE key = :'key';
SQL
    ;;
  set)
    psql "$DBURL" -X -At -q -v key="$2" -v value="$3" <<'SQL'
INSERT INTO audit_state (key, value) VALUES (:'key', :'value')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
RETURNING key || ' saved';
SQL
    ;;
  *) echo "usage: audit_state.sh get <key> | set <key> <value>" >&2; exit 2 ;;
esac
