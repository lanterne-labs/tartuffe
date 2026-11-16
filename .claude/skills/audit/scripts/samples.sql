-- One random sample of active facts for the audit, as tab-separated rows:
-- id, persons (a * marks a candidate), verb, statement date/precision, source
-- type, source date, URL, fact text, quote. Grouped duplicates are left out.
-- Usage (psql -X -At -F $'\t'):
--   -v seed=0.1116 -v n=120 -v types="'news','twitter','viepublique'" \
--   -v cond="f.created_at > '2026-11-14 13:45+00'"          the new window
--   -v cond="f.id >= 65992 and f.id < 73992"                 the history band
--   -v types="'assemblee_law_page'" / "'assemblee_debat'"    votes, debates
select setseed(:seed);
select f.id,
  coalesce((select string_agg(distinct p.name || case when p.attributes->>'candidate' = 'true' then '*' else '' end, ', ')
            from fact_sources s2 join fact_mentions m on m.fact_source_id = s2.id join persons p on p.id = m.person_id
            where s2.fact_id = f.id), '?'),
  f.canonical_verb,
  coalesce(f.statement_date::text, '-') || '/' || coalesce(f.statement_date_precision, '-'),
  s.source_type, coalesce(to_char(s.published_at, 'YYYY-MM-DD'), '-'), s.source_url,
  replace(f.fact_text, E'\n', ' '), replace(left(coalesce(s.original_quote, ''), 500), E'\n', ' ')
from facts f
join lateral (select * from fact_sources s where s.fact_id = f.id order by published_at nulls last limit 1) s on true
where not exists (select 1 from fact_relations r where r.specific_fact_id = f.id and r.relation_type = 'duplicate')
  and s.source_type in (:types) and (:cond)
order by random() limit :n;
