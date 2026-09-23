package storage

import (
	"context"
	"fmt"
)

// A fact that stands for a group of restatements — 'duplicate' fact_relations
// from the fact (general_fact_id) to each member (specific_fact_id) — is
// judged with its members' wordings by every contradiction judge and veto
// gate (promptrules.Statement.Also).

// concretenessOrder ranks fact texts most concrete first: most digits
// (figures, amounts, dates), then longest. %[1]s is the text expression. The
// single definition keeps the parent choice and the order members are shown
// to a judge in agreement.
const concretenessOrder = `length(regexp_replace(%[1]s, '[^0-9]', '', 'g')) DESC, length(%[1]s) DESC`

// groupMemberBudgetChars bounds the other wordings a judge is shown per
// statement. 800 characters covers every member of 97 % of groups (measured
// 2026-09-22: 92 % have at most 3 members, ≤ 650 chars); larger groups keep
// their most concrete members.
const groupMemberBudgetChars = 800

// GroupMemberTexts returns, for each of factIDs that is a group parent, the
// distinct texts of its members, most concrete first (most digits, then
// longest), within groupMemberBudgetChars — the first member always, however
// long. Facts without members are absent from the map. One round trip.
func (s *PostgresStorer) GroupMemberTexts(ctx context.Context, factIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(factIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		WITH m AS (
			SELECT DISTINCT r.general_fact_id AS parent, f.fact_text AS text
			FROM fact_relations r
			JOIN facts f ON f.id = r.specific_fact_id
			WHERE r.relation_type = 'duplicate' AND r.general_fact_id = ANY($1)
		), ranked AS (
			SELECT parent, text,
			       row_number() OVER w AS rn,
			       SUM(length(text)) OVER (w ROWS UNBOUNDED PRECEDING) AS cum
			FROM m
			WINDOW w AS (PARTITION BY parent ORDER BY `+concretenessOrder+`, text)
		)
		SELECT parent, text FROM ranked
		WHERE rn = 1 OR cum <= $2
		ORDER BY parent, rn`, "text"), factIDs, groupMemberBudgetChars)
	if err != nil {
		return nil, fmt.Errorf("loading group member texts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var parent int64
		var text string
		if err := rows.Scan(&parent, &text); err != nil {
			return nil, fmt.Errorf("scanning group member text: %w", err)
		}
		out[parent] = append(out[parent], text)
	}
	return out, rows.Err()
}
