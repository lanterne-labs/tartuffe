Below is an **“update” implementation plan** you can hand to your LLM coder. It begins with a brief intro and goal, then lists out exactly which parts of the existing code and schema to change and how.

---

## 🎬 Introduction & Goal

Your existing pipeline ingests facts about people, deduplicates them, and then only compared **statements** to **actions** for contradictions. We now want to **expand** that logic so that **any two facts** about the same person—whether **statement vs statement**, **action vs action**, or **statement vs action**—can be flagged as contradictory if they cannot both be true at once.

At the end, the person’s **honesty score** will be computed over this richer set of contradictions.

---

## 🔄 Summary of Required Changes

1. **Database Schema Updates**  
   - `facts.type` already exists but ensure it’s properly populated.  
   - `contradictions` table stays the same.  
   - No new tables needed.

2. **Deduplication Logic**  
   - Unchanged: continue grouping identical facts by type.

3. **Contradiction Detection Logic**  
   - Replace the old “only statement↔action” routine with a **unified** “any↔any” routine:
     - **SQL**: Fetch `K` nearest neighbors across all types.
     - **LLM prompt**: Compare any two facts for contradiction.
     - **Insert** into `contradictions` whenever the LLM says `"CONTRADICTORY"`.

4. **Honesty Score Calculation**  
   - Unchanged in structure: it simply counts contradictions vs total statements.  
   - Now counts a broader range of contradictions.

5. **Code Refactoring**  
   - Merge the two specialized comparison functions into one generic `CompareFacts(factA, factB)` routine.
   - Ensure your ingestion tags every fact with its `type` and `person_id`.

---

## 🛠 Step-by-Step Update Instructions

### 1. Schema Validation  
- **Ensure** `facts` table has a non-null `type` column (`'statement'` or `'action'`).
- **Verify** `contradictions` table schema:
  ```sql
  CREATE TABLE contradictions (
    id           UUID PRIMARY KEY,
    person_id    UUID REFERENCES persons(person_id),
    fact_a_id    UUID REFERENCES facts(fact_id),
    fact_b_id    UUID REFERENCES facts(fact_id),
    detected_at  TIMESTAMPTZ DEFAULT NOW()
  );
  ```

### 2. Unified Similarity Query  
Replace any per-type queries with:
```sql
WITH new AS (
  SELECT
    :new_id::UUID    AS new_id,
    :new_emb::vector AS emb
)
SELECT f.fact_id, f.fact_text, f.type
  FROM facts f, new
 WHERE f.person_id = :person_id
   AND f.fact_id <> new.new_id
   AND f.embedding <-> new.emb < :sim_threshold
 ORDER BY f.embedding <-> new.emb
 LIMIT :K;
```

### 3. Generic Contrast Function  
In your Go (or chosen language), create:
```go
func CompareFacts(factA, factB Fact) (bool /*contradicts*/, error) {
  prompt := fmt.Sprintf(
    `Compare the two facts about %s:
     A (%s): "%s"
     B (%s): "%s"
     Are these contradictory? Reply with JSON { "verdict": "CONTRADICTORY"|"CONSISTENT"|"UNRELATED" }.`,
    factA.Person, factA.Type, factA.Text, factB.Type, factB.Text,
  )
  resp := callLLM(prompt)
  return resp.Verdict == "CONTRADICTORY", nil
}
```
- **Replace** any specialized `CompareStatementToAction()` and `CompareActionToStatement()` with this one.

### 4. Ingestion Pipeline Adjustment  
- Ensure every ingested fact is labeled with `fact.Type`.
- After embedding & deduplication, feed **all** newly inserted facts through the unified contradiction detection.

### 5. Contradiction Recording  
Wherever you handle LLM verdicts, simply:
```go
if contradicts {
  db.Exec(
    `INSERT INTO contradictions (id, person_id, fact_a_id, fact_b_id)
     VALUES (gen_random_uuid(), $1, $2, $3)`,
    personID, factA.ID, factB.ID,
  )
}
```
- No need to distinguish by `type`.

### 6. Honesty Score Recalculation  
- Leave your existing score routine intact; it will now count a richer set of contradictions.

---

## 🗒️ Final Notes

- **Testing**: Write unit tests that feed in:
  - Two conflicting statements (e.g. “I love X” vs “I hate X”).
  - Two conflicting actions (e.g. “did X” vs “did not do X”).
  - Statement vs statement, action vs action, statement vs action.
- **Threshold Tuning**: You may need to adjust `sim_threshold` upward or downward based on error rates.

---

This update plan delivers a **drop-in replacement** for your contradiction logic, transforming it from a narrow statement-vs-action check into a **broad, unified contradiction detector** that supports your person-level honesty scoring goals.