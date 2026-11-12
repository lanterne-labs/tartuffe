-- The candidates' active news facts whose article (title + stored text) never
-- names the person: words of someone else given to the target (2026-11-12:
-- an interview of Philippe Juvin and four Instagram posts stored as Nicolas
-- Dupont-Aignan's, two answers of Dominique de Villepin as François Ruffin's).
-- Read every row: a page whose text was fetched again later (a live page, an
-- aggregator) and a person's own site are listed too and are fine.
with cand as (select id, name, regexp_replace(name,'^.* ','') as last from persons where attributes->>'candidate'='true')
select f.id, c.name, length(d.content), s.source_url, replace(coalesce(d.title,''),'|','/'), left(f.fact_text,230), left(regexp_replace(s.original_quote,'\s+',' ','g'),200)
from cand c join fact_mentions m on m.person_id=c.id join fact_sources s on s.id=m.fact_source_id join facts f on f.id=s.fact_id
join source_documents d on d.url=s.source_url
where s.source_type='news' and length(d.content)>400
  and not exists (select 1 from fact_relations r where r.specific_fact_id=f.id and r.relation_type='duplicate')
  and position(c.last in lower(translate(coalesce(d.title,'')||' '||d.content||' '||s.source_url,'éèêëÉÈàâîïôöùûüçÀÂÎÔ','eeeeEEaaiioouuucAAIO')))=0
order by c.name, f.id;
