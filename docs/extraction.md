Sure — here’s a full rewrite of **Section 1.1: Fact Discovery and Web Scraping**, based on all our decisions and discussions so far. This is designed to be handed off directly to an LLM agent for execution.

---

### ✅ **1.1. Fact Discovery and Web Scraping**

#### 🔹 **Goal**

Discover recent online content that mentions or quotes public figures (e.g., politicians), extract structured factual statements from that content, and persist them in a database along with relevant metadata (e.g., person involved, source URL, timestamp, topic domain).

This pipeline must:
- Accept a list of names.
- Proactively search for new articles and content about each name regularly (e.g., daily).
- Extract content from JS-heavy sites reliably.
- Output structured, LLM-friendly facts (e.g., `"Person X said they want to cut taxes"`) and store them in the DB.
- Be fully testable locally and modular, with each part swappable (e.g., search engine, scraping service).

The implementation language is **Go**.

---

#### 🔹 **Structure & Components**

##### 🧱 **1.1.1. Person Discovery Loop**

**Input**: A list of names from the database or static config.  
**Behavior**:  
For each name, do the following:

1. **Search the web for recent content mentioning this person**.  
   Abstract this behind a `SearchProvider` interface:
   ```go
   type SearchProvider interface {
       Search(personName string) ([]SearchResult, error)
   }
   ```

2. **Recommended providers** (behind the interface):
   - ✅ **Google Custom Search JSON API** (paid, $5/1k queries) – best quality.
   - ✅ **Serper.dev** or **Brave Search API** – affordable alternatives.
   - ❌ Avoid SerpAPI for now (overpriced at scale unless sponsored).

3. **Return a list of URLs** to pass to the scraper.

##### 🧱 **1.1.2. Web Scraping & Content Extraction**

**Input**: List of URLs from the search step.  
**Goal**: Extract clean text content from JS-heavy web pages for LLM processing.

Abstract this behind a `ContentExtractor` interface:
```go
type ContentExtractor interface {
    ExtractContent(url string) (ExtractedContent, error)
}
```

**Recommended Providers**:
- ✅ **Apify** (use "LLM Web Scraper" actor)  
   - Fully remote, handles JS, includes metadata extraction.  
   - Output is already structured enough for LLM.  
   - Pay-as-you-go pricing. You can hide this behind an HTTP client in Go.

- ✅ **Browserless.io** with `@puppeteer/extra-plugin-stealth` (if building in-house in the future).
- ✅ **Playwright-go** (local alternative for full control later, not for now).

**Output format from scraper**:
```json
{
  "url": "https://example.com/article",
  "title": "Person X makes tax reform announcement",
  "date_published": "2024-04-19",
  "content": "In a recent interview, Person X said they plan to cut income taxes by 10%..."
}
```

##### 🧱 **1.1.3. Fact Extraction from Content**

**Input**: Clean article text.  
**Goal**: Extract 1–n factual claims that can be compared later.

Behind a `FactExtractor` interface:
```go
type FactExtractor interface {
    ExtractFacts(content ExtractedContent) ([]Fact, error)
}
```

**Implementation**:
Use an LLM (via remote API) with a carefully crafted prompt that:
- Summarizes each statement involving the target person.
- Outputs JSON like:
```json
{
  "statement": "Person X said they plan to cut income taxes by 10%",
  "topics": ["tax", "economy", "public finance"],
  "person": "Person X",
  "source_url": "...",
  "published_at": "...",
  "embedding": [0.021, -0.134, ...] // optional, filled later
}
```

**Recommended LLM providers**:
- ✅ **OpenAI GPT-4 / GPT-3.5 Turbo** – robust instruction following.
- ✅ **Anthropic Claude 3** – strong comprehension, high context.
- ✅ **Mistral via Hugging Face Inference API** – if looking for OSS later.

This should **not store the full article text**, only the summarized facts and their source URLs.

---

#### 🔹 **Output & Storage**

All extracted facts should be saved in a PostgreSQL DB (hosted by Supabase or the host), with the following schema:

```sql
CREATE TABLE facts (
    id UUID PRIMARY KEY,
    person TEXT NOT NULL,
    statement TEXT NOT NULL,
    topics TEXT[],
    source_url TEXT,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    embedding VECTOR(1536) -- optional, filled by separate step
);
```

---

#### 🔹 **Tooling & Local Dev**

- Use **Docker Compose** to run Postgres locally.
- Abstract all external APIs (search, scraping, LLM) behind Go interfaces.
- Add a CLI command: `cmd/fact-pipeline/main.go` to test the pipeline locally.
- Enable dry-run mode to just print results instead of writing to DB.

---

#### ✅ Summary of Deliverables for Section 1.1

| Component | Goal | Language | Test Locally? | Remote Alternative |
|----------|------|----------|----------------|--------------------|
| Search Provider | Discover new URLs for each person | Go interface | ✅ Yes (mocked) | Google Custom Search / Serper |
| Scraper | Get article content from URL | Go interface | ✅ with mocks | Apify |
| Fact Extractor | Extract structured facts using LLM | Go + API call | ✅ via dummy input | OpenAI / Claude |
| DB Storage | Persist facts | SQL | ✅ local Postgres | Supabase / the host Postgres |

Let me know if you want this chunk converted into code scaffolding to bootstrap development.