package storage

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"

	"github.com/rs/zerolog/log"
)

// A claim group is a set of facts in which the same person makes the same
// claim. It is stored as 'duplicate' fact_relations from the group's parent
// (general_fact_id) to each member (specific_fact_id): the parent is an
// actual statement — the group's most concrete wording — and is the fact the
// site shows, the score counts and contradictions point at; the members are
// hidden from those and handed to every contradiction judge alongside the
// parent (GroupMemberTexts).

// MostConcreteFact returns, among factIDs, the fact with the most concrete
// text (ties: lowest id).
func (s *PostgresStorer) MostConcreteFact(ctx context.Context, factIDs []int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT id FROM facts WHERE id = ANY($1)
		ORDER BY `+concretenessOrder+`, id
		LIMIT 1`, "fact_text"), factIDs).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("picking the most concrete of %v: %w", factIDs, err)
	}
	return id, nil
}

// MergeFactIntoGroup makes from — and every member of from's group — members
// of into's group, in one transaction: the members move under into, from
// becomes a member, contradictions on from move to into (a pair that already
// exists on into, or a from-into self-pair, is dropped), group-base
// references follow, and into's cached pair verdicts are dropped (the judges
// now see more wordings for it). Stance slots: into joins from's stances for
// any person it has none with — and when into was a member of from's group
// (a promotion), its own stance links were invisible, so it takes from's
// visible slot instead. Stance key facts are rewritten from → into and kept
// to facts the stance actually holds. into must be active (not a member of
// any other group).
func (s *PostgresStorer) MergeFactIntoGroup(ctx context.Context, from, into int64) error {
	if from == into {
		return fmt.Errorf("merging fact %d into its own group", from)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group merge: %w", err)
	}
	defer tx.Rollback(ctx)

	// into may be a member of from (a member becoming the parent); from's
	// other members move under into.
	tag, err := tx.Exec(ctx, `
		DELETE FROM fact_relations
		WHERE general_fact_id = $1 AND specific_fact_id = $2 AND relation_type = 'duplicate'`, from, into)
	if err != nil {
		return fmt.Errorf("detaching %d from %d: %w", into, from, err)
	}
	promoted := tag.RowsAffected() > 0
	var intoIsMember bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM fact_relations WHERE specific_fact_id = $1 AND relation_type = 'duplicate')`, into).Scan(&intoIsMember); err != nil {
		return fmt.Errorf("checking %d is active: %w", into, err)
	}
	if intoIsMember {
		return fmt.Errorf("fact %d is a member of another group; merge into its parent instead", into)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fact_relations SET general_fact_id = $2
		WHERE general_fact_id = $1 AND relation_type = 'duplicate'`, from, into); err != nil {
		return fmt.Errorf("moving the members of %d under %d: %w", from, into, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		VALUES ($1, $2, 'duplicate', 'same claim')
		ON CONFLICT (general_fact_id, specific_fact_id) DO UPDATE SET relation_type = 'duplicate'`, into, from); err != nil {
		return fmt.Errorf("making %d a member of %d: %w", from, into, err)
	}

	// A promoted member's own stance links were hidden with it; the group's
	// visible slot is from's, so they give way to it for every person from
	// has a stance with.
	if promoted {
		if _, err := tx.Exec(ctx, `
			DELETE FROM stance_document_facts sdf
			USING stance_documents sd
			WHERE sdf.fact_id = $2 AND sd.id = sdf.stance_document_id
			  AND sd.person_id IN (
			      SELECT d2.person_id FROM stance_document_facts s2
			      JOIN stance_documents d2 ON d2.id = s2.stance_document_id
			      WHERE s2.fact_id = $1)`, from, into); err != nil {
			return fmt.Errorf("clearing the hidden stance links of %d: %w", into, err)
		}
	}
	// into takes from's stance slot for every person it has no stance with.
	if _, err := tx.Exec(ctx, `
		INSERT INTO stance_document_facts (stance_document_id, fact_id)
		SELECT sdf.stance_document_id, $2
		FROM stance_document_facts sdf
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE sdf.fact_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM stance_document_facts s2
		      JOIN stance_documents d2 ON d2.id = s2.stance_document_id
		      WHERE s2.fact_id = $2 AND d2.person_id = sd.person_id)
		ON CONFLICT DO NOTHING`, from, into); err != nil {
		return fmt.Errorf("moving the stance slot of %d to %d: %w", from, into, err)
	}
	// Stance key facts name facts the stance holds: from → into, first
	// occurrence's place kept, then anything the stance no longer holds
	// (into in a stance it left) dropped.
	if _, err := tx.Exec(ctx, `
		UPDATE stance_documents sd
		SET key_fact_ids = (
			SELECT COALESCE(array_agg(x ORDER BY first_ord), '{}')
			FROM (SELECT x, MIN(ord) AS first_ord
			      FROM unnest(array_replace(sd.key_fact_ids, $1::bigint, $2::bigint)) WITH ORDINALITY AS t(x, ord)
			      WHERE EXISTS (SELECT 1 FROM stance_document_facts l
			                    WHERE l.stance_document_id = sd.id AND l.fact_id = t.x)
			      GROUP BY x) u)
		WHERE sd.key_fact_ids && ARRAY[$1::bigint, $2::bigint]`, from, into); err != nil {
		return fmt.Errorf("replacing %d by %d in stance key facts: %w", from, into, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM fact_pair_checks WHERE fact_a_id = $1 OR fact_b_id = $1`, into); err != nil {
		return fmt.Errorf("re-opening the pair verdicts of %d: %w", into, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE contradictions SET base_fact_id = $2 WHERE base_fact_id = $1`, from, into); err != nil {
		return fmt.Errorf("moving contradiction group bases off %d: %w", from, err)
	}
	if _, _, err := s.repointFactDuplicateTx(ctx, tx, into, from); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group merge: %w", err)
	}
	return nil
}

// ListClaimGroups returns the groups (active parents with at least one
// member) that gained a member at or after since, with the parent's person
// and text, oldest parent first; onlyIDs, when non-empty, restricts it to
// those parents. Reads the groups, not the facts table.
func (s *PostgresStorer) ListClaimGroups(ctx context.Context, since time.Time, onlyIDs []int64) ([]core.ClaimGroup, error) {
	rows, err := s.pool.Query(ctx, `
		WITH g AS (
			SELECT general_fact_id AS id FROM fact_relations
			WHERE relation_type = 'duplicate'
			  AND (coalesce(cardinality($2::bigint[]), 0) = 0 OR general_fact_id = ANY($2))
			GROUP BY general_fact_id
			HAVING MAX(created_at) >= $1
		)
		SELECT p.id, COALESCE(fm.person_id, 0), COALESCE(pe.name, ''), p.fact_text
		FROM g
		JOIN facts p ON p.id = g.id
		LEFT JOIN LATERAL (
			SELECT fm.person_id FROM fact_sources fs
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fs.fact_id = p.id ORDER BY fs.id LIMIT 1) fm ON true
		LEFT JOIN persons pe ON pe.id = fm.person_id
		WHERE NOT EXISTS (
			SELECT 1 FROM fact_relations r
			WHERE r.specific_fact_id = p.id AND r.relation_type = 'duplicate')
		ORDER BY p.id`, since, onlyIDs)
	if err != nil {
		return nil, fmt.Errorf("listing claim groups: %w", err)
	}
	defer rows.Close()
	var out []core.ClaimGroup
	for rows.Next() {
		var g core.ClaimGroup
		if err := rows.Scan(&g.ParentID, &g.PersonID, &g.PersonName, &g.Text); err != nil {
			return nil, fmt.Errorf("scanning claim group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetGroupMembers returns the parent's members (id and text), oldest first.
func (s *PostgresStorer) GetGroupMembers(ctx context.Context, parentID int64) ([]core.FactForFold, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fact_text FROM fact_relations r
		JOIN facts f ON f.id = r.specific_fact_id
		WHERE r.general_fact_id = $1 AND r.relation_type = 'duplicate'
		ORDER BY f.id`, parentID)
	if err != nil {
		return nil, fmt.Errorf("loading members of group %d: %w", parentID, err)
	}
	defer rows.Close()
	var out []core.FactForFold
	for rows.Next() {
		var f core.FactForFold
		if err := rows.Scan(&f.ID, &f.Text); err != nil {
			return nil, fmt.Errorf("scanning group member: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UngroupFact makes a member of the parent's group active again. A member has
// no stance slot of its own while grouped, so it takes the parent's stance
// for every person it has none with — it was judged the same claim, so the
// same subject. Contradictions stay on the parent; the nightly detection
// judges the freed fact as any other active fact.
func (s *PostgresStorer) UngroupFact(ctx context.Context, parentID, factID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ungroup: %w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		DELETE FROM fact_relations
		WHERE general_fact_id = $1 AND specific_fact_id = $2 AND relation_type = 'duplicate'`, parentID, factID)
	if err != nil {
		return fmt.Errorf("ungrouping fact %d from %d: %w", factID, parentID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact %d is not a member of group %d", factID, parentID)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO stance_document_facts (stance_document_id, fact_id)
		SELECT sdf.stance_document_id, $2
		FROM stance_document_facts sdf
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE sdf.fact_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM stance_document_facts s2
		      JOIN stance_documents d2 ON d2.id = s2.stance_document_id
		      WHERE s2.fact_id = $2 AND d2.person_id = sd.person_id)
		ON CONFLICT DO NOTHING`, parentID, factID); err != nil {
		return fmt.Errorf("giving fact %d the stance of %d: %w", factID, parentID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ungroup: %w", err)
	}
	log.Info().Int64("fact_id", factID).Int64("parent_fact_id", parentID).Msg("Ungrouped fact")
	return nil
}
