# News Search Examples

The news search functionality is now a standalone command under `plugin news search`, making it generic for any domain (not just politics).

## Basic News Search

Search news for all persons in the database:
```bash
./digital-mirror plugin news search --max-parse 5
```

This will find up to 5 articles per person and try to parse all of them to extract facts.

## Search Specific Targets

Search news for specific people/parties (with fuzzy matching):
```bash
./digital-mirror plugin news search \
  --targets "Marine Pen,Macron,Melenchon" \
  --max-parse 10
```

## Control How Many Articles to Parse

The `--max-parse` flag controls how many articles to successfully parse per target:

```bash
# Test with just 2 articles per person
./digital-mirror plugin news search \
  --targets "Emmanuel Macron" \
  --max-parse 2

# Process more articles for production
./digital-mirror plugin news search \
  --max-parse 20
```

The system will:
1. Request `--max-parse` articles from SerpAPI for each target
2. Try to parse all of them to extract facts
3. Stop when it has successfully parsed that many articles OR runs out of articles
4. If some articles fail to parse (404, paywall, etc.), it continues to the next ones

## Creating Missing Persons Interactively

When you search for a person that doesn't exist in the database, you'll be prompted to create them:

```bash
./digital-mirror plugin news search \
  --targets "Édouard Philippe"
```

Output:
```
Some target names need disambiguation or don't exist yet:

Query: "Édouard Philippe"
No matches found in database.
Create this person/party? (y=person, p=party, n=skip): y
Created new person: Édouard Philippe (ID: 42)

Re-running with resolved targets: [Édouard Philippe]
...
```

## Fuzzy Name Matching

The system handles variations in names:

```bash
./digital-mirror plugin news search \
  --targets "Marine Pen,melenchon,MACRON" \
  --max-parse 5
```

- "Marine Pen" → matches "Marine Le Pen" (missing article)
- "melenchon" → matches "Jean-Luc Mélenchon" (case-insensitive, missing accents)
- "MACRON" → matches "Emmanuel Macron" (case-insensitive)

## Disambiguation

When a query matches multiple persons:

```bash
./digital-mirror plugin news search \
  --targets "Marine" \
  --max-parse 5
```

Output:
```
Query: "Marine"
Found 2 potential matches:
  1) Marine Le Pen (person) - similarity: 0.95
  2) Marine Tondelier (person) - similarity: 0.92
  0) Skip this target
Enter your choice (0-2): 1
Selected: Marine Le Pen
```

## Date Filtering

Search news published after a specific date:

```bash
./digital-mirror plugin news search \
  --since 2024-01-01 \
  --max-parse 10
```

## Skip Unknown Targets

Skip persons with no prior news search results (saves API calls):

```bash
./digital-mirror plugin news search \
  --skip-unknown \
  --max-parse 5
```

## Politics-Specific: Combined Mode

The frpolitics plugin still supports combined mode for official votes + news:

```bash
./digital-mirror plugin frpolitics --mode both \
  --max-laws 10 \
  --news-limit 5 \
  --news-location France
```
