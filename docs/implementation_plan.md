Got it! Here's a **detailed implementation plan** that expands on each step of the process, explaining exactly what to do, how to structure the data, how to process it, and how to store it. I'll also provide specific instructions on how to test everything locally and deploy it to **the host**, with as much detail as possible.

---

### **Detailed Implementation Plan**

---

### **1. Fact Discovery and Categorization**

This section will outline the steps for discovering facts, scraping web content, and categorizing it for later analysis.

#### **1.1. Fact Discovery and Web Scraping**
Very similar to https://python.langchain.com/v0.1/docs/use_cases/web_scraping/
- **Goal**: Fetch content from websites (news articles, public statements, etc.) that could contain facts related to a person, event, or topic.
- **Steps**:
  1. **API Scraping (Alternative to Web Scraping)**:
     - Use **Diffbot** or **ScrapingBee** to extract structured data from web pages.
     - These APIs will return clean, structured data, ideal for further processing and analysis.
     - Example API call:
       ```bash
       curl -H "Authorization: Bearer YOUR_API_KEY" \
       "https://api.diffbot.com/v3/article?url=YOUR_URL"
       ```
       This will return the article's text content, metadata (e.g., title, author, date), and images in a structured format (JSON).
     - **Documentation**:
       - [Diffbot API Docs](https://www.diffbot.com/products/)
       - [ScrapingBee API Docs](https://www.scrapingbee.com/docs/)
  
  2. **Web Scraping with Playwright or Puppeteer** (for dynamic content):
     - Use **Playwright** (recommended for modern, high-performance scraping) or **Puppeteer** (works well with Chrome/Chromium) for scraping dynamic content.
     - **Example Playwright Scraper**:
       ```javascript
       const { webkit } = require('playwright');
       const browser = await webkit.launch();
       const page = await browser.newPage();
       await page.goto('https://example.com');
       const content = await page.content();  // Extracts raw HTML content
       console.log(content);
       await browser.close();
       ```

- **Data Format**:
  - Store scraped data in JSON format, with relevant fields such as:
    ```json
    {
      "url": "https://example.com/article",
      "title": "Fact about public spending",
      "content": "The politician X expressed an opinion about public spending...",
      "author": "John Doe",
      "date": "2023-04-12",
      "tags": ["public spending", "politician X"]
    }
    ```

#### **1.2. Fact Categorization**
- **Goal**: Classify facts based on their content (e.g., topic, involved persons, etc.) for later retrieval.
- **Approach**:
  1. Use a **pre-trained LLM** for **text classification** (e.g., OpenAI's GPT-3, Cohere, or Hugging Face) to categorize the scraped data. 
  2. Define categories based on the most common topics relevant to your project (e.g., economy, public spending, education).
  3. Store these categories along with the scraped facts.

- **Example Classification** (via OpenAI's GPT-3):
  - Input: `"Politician X wants to cut taxes on education."`
  - Output: `"Category: Public Spending, Education"`
  - Use the following code to interact with OpenAI:
    ```go
    openai := NewOpenAIClient("your-api-key")
    response := openai.Completion("classify this statement: 'Politician X wants to cut taxes on education'")
    category := response.Text
    ```
  
  4. **Data Storage Format**:
     - Add the `category` field to the fact's metadata:
       ```json
       {
         "url": "https://example.com/article",
         "title": "Fact about public spending",
         "content": "The politician X expressed an opinion about public spending...",
         "author": "John Doe",
         "date": "2023-04-12",
         "tags": ["public spending", "politician X"],
         "category": "Public Spending, Education"
       }
       ```

#### **1.3. Fact Extraction and Categorization Process**
- Write a script to perform scraping, send the content to the LLM API for classification, and save the data in a local database (PostgreSQL via Supabase).
- **Data Flow**:
  1. Fetch raw content (via API or scraping).
  2. Process the content (remove stopwords, clean the text).
  3. Classify the text into predefined categories.
  4. Store the result in Supabase database.

- **Local Setup**:
  - **Supabase** will be used to store facts and classifications. You will use **pgvector** for storing text embeddings for quick search later.
  - **Example Go Code**:
    ```go
    // Insert fact into Supabase DB
    db.Exec("INSERT INTO facts (title, content, category, tags) VALUES ($1, $2, $3, $4)", title, content, category, tags)
    ```

---

### **2. Database System (PostgreSQL with Supabase)**

This section explains how to set up and interact with your database locally and on the host.

#### **2.1. Setting Up Supabase Locally (via Docker)**

- **Supabase Initialization**: Run Supabase locally using Docker.
  ```bash
  supabase init
  supabase start
  ```

- **PostgreSQL Database**: Supabase provides PostgreSQL and the `pgvector` extension for embedding storage.

#### **2.2. Database Schema Design**
Design a schema for storing normalized facts, their original sources, mentioned persons, and embeddings, following the simplified approach.

- **Facts Table (`facts`)**: Stores the unique, normalized core factual statement and its embedding.
  ```sql
  -- Table to store unique, normalized facts
  CREATE TABLE facts (
      -- Unique identifier for the fact
      id BIGSERIAL PRIMARY KEY,
      -- The normalized, core factual statement (unique)
      fact_text TEXT NOT NULL UNIQUE,
      -- Vector embedding of the fact_text (dimension depends on your model, e.g., 1536)
      embedding vector(1536) NOT NULL,
      -- Optional: Relevant topics or categories associated with the fact
      topics TEXT[],
      -- Timestamp of when the fact was first created
      created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
      -- Timestamp of when the fact was last updated (e.g., embedding refined)
      updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
  );

  -- Add an index for efficient vector similarity search (e.g., HNSW)
  CREATE INDEX ON facts USING hnsw (embedding vector_l2_ops);

  -- Trigger to automatically update updated_at timestamp
  CREATE OR REPLACE FUNCTION trigger_set_timestamp()
  RETURNS TRIGGER AS $$
  BEGIN
      NEW.updated_at = NOW();
      RETURN NEW;
  END;
  $$ LANGUAGE plpgsql;

  CREATE TRIGGER set_facts_timestamp
  BEFORE UPDATE ON facts
  FOR EACH ROW
  EXECUTE FUNCTION trigger_set_timestamp();
  ```

- **Persons Table (`persons`)**: Stores unique, normalized person names.
  ```sql
  -- Table to store unique persons mentioned
  CREATE TABLE persons (
      -- Unique identifier for the person
      id BIGSERIAL PRIMARY KEY,
      -- Normalized name of the person (unique)
      name TEXT NOT NULL UNIQUE,
      -- Timestamp of when the person was first added
      created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
  );
  ```

- **Fact Sources Table (`fact_sources`)**: Links a normalized fact to a specific source URL and the original quote from that source.
  ```sql
  -- Table to link facts to their specific sources (URLs) and original quotes
  CREATE TABLE fact_sources (
      -- Unique identifier for this specific source instance
      id BIGSERIAL PRIMARY KEY,
      -- Foreign key referencing the normalized fact
      fact_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
      -- The URL where the fact was found
      source_url TEXT NOT NULL,
      -- The original, non-normalized quote or text snippet from the source
      original_quote TEXT,
      -- Publication date of the source, if available
      published_at TIMESTAMP WITH TIME ZONE,
      -- Timestamp of when this source was scraped/processed
      scraped_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
      -- Ensure a source URL is linked only once to a specific fact
      UNIQUE (fact_id, source_url)
  );

  -- Index for faster lookups by URL (useful for pre-checking)
  CREATE INDEX idx_fact_sources_url ON fact_sources(source_url);
  -- Index for faster lookups by fact_id
  CREATE INDEX idx_fact_sources_fact_id ON fact_sources(fact_id);
  ```

- **Fact Mentions Table (`fact_mentions`)**: Links persons to specific mentions within a fact source.
  ```sql
  -- Junction table to link persons mentioned in a specific source quote
  CREATE TABLE fact_mentions (
      -- Unique identifier for the mention link
      id BIGSERIAL PRIMARY KEY,
      -- Foreign key referencing the specific source instance
      fact_source_id BIGINT NOT NULL REFERENCES fact_sources(id) ON DELETE CASCADE,
      -- Foreign key referencing the person mentioned
      person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
      -- Ensure a person is linked only once to a specific fact source
      UNIQUE (fact_source_id, person_id)
  );

  -- Indexes for faster lookups/joins
  CREATE INDEX idx_fact_mentions_fact_source_id ON fact_mentions(fact_source_id);
  CREATE INDEX idx_fact_mentions_person_id ON fact_mentions(person_id);
  ```

#### **2.3. Inserting Data into the Database**
- The `StoreFact` function in the Go storage layer encapsulates the logic:
  - **Input**: Takes a `StoreFactInput` struct containing `NormalizedFactText`, `Embedding`, `OriginalQuote`, `SourceURL`, `PublishedAt`, `Topics`, `InvolvedPeople`.
  - **Transaction**: All operations run within a single database transaction.
  - **Step A (Find/Insert Fact)**:
    - Searches `facts` table using the `Embedding` for similarity.
    - Checks top candidates for matching `NormalizedFactText`.
    - If match found, uses existing `fact_id`.
    - If no match, inserts into `facts` (`NormalizedFactText`, `Embedding`, `Topics`) and gets new `fact_id`.
  - **Step B (Find/Insert Persons)**:
    - Upserts each normalized name from `InvolvedPeople` into `persons` table, retrieving `person_id`s.
  - **Step C (Link Source to Fact)**:
    - Inserts into `fact_sources` (`fact_id`, `SourceURL`, `OriginalQuote`, `PublishedAt`) using `ON CONFLICT (fact_id, source_url)` to handle updates. Retrieves `fact_source_id`.
  - **Step D (Link Persons to Source Mention)**:
    - Inserts into `fact_mentions` linking the `fact_source_id` and each `person_id`, using `ON CONFLICT DO NOTHING`.
- **URL Pre-check**: A helper function `CheckSourceURLExists` queries `fact_sources` by `source_url` and can be used *before* extraction to skip already processed URLs.

---

### **3. Fact Comparison and Contradiction Detection**

This section explains how to use embeddings to detect contradictions between facts.

#### **3.1. Embedding Generation for Fact Comparison**

- **Generate Embeddings**:
  - Use OpenAI, Cohere, or Hugging Face for generating embeddings from the facts.
  - Use the `text-embedding-ada-002` model (or equivalent) to generate embeddings.
  - **Example**:
    ```go
    // Generate embedding using OpenAI's API
    embedding := openai.GetEmbedding("The politician X wants to cut taxes on education.")
    storeEmbeddingInDb(embedding)
    ```

- **Cosine Similarity Search**: Store embeddings in the database and use cosine similarity to find facts that are most similar (i.e., related) to the new incoming fact.

#### **3.2. Detecting Contradictions**
- **Contradiction Detection**: Compare the embeddings of the new fact with those already stored in the database. If the similarity score is low and the category/topic is the same, flag it as contradictory.
  - Example SQL query to find similar canonical facts based on embedding:
    ```sql
    SELECT id, assertion, sentiment, embedding <-> $1 AS distance 
    FROM canonical_facts
    ORDER BY distance
    LIMIT 10; -- Find top 10 potential matches by vector similarity
    ```
  - The application logic then needs to compare the `assertion` and `sentiment` of the potential matches to determine if it's a true duplicate, a contradiction, or unrelated.

#### **3.3. Categorization-Based Filtering**
- Filter facts by category (e.g., if the new fact is about public spending, only compare it with other facts in the `public spending` category).
  - This reduces the comparison scope and optimizes the process.

---

### **4. Truth Score Tracking**

This section tracks the truth score of each fact over time.

#### **4.1. Truth Score Calculation**
- Calculate a score based on how often a fact is contradicted.
- For each fact, store the truth score in a separate table.
  ```sql
  CREATE TABLE truth_scores (
    canonical_fact_id INTEGER REFERENCES canonical_facts(id) ON DELETE CASCADE,
    score FLOAT,
    last_updated TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (canonical_fact_id)
  );
  ```

#### **4.2. Updating Truth Scores**
- When a new contradiction is detected, update the fact's truth score. For example, if a fact is contradicted, reduce the score:
  ```go
  db.Exec("UPDATE truth_scores SET score = score - 0.1, last_updated = NOW() WHERE canonical_fact_id = $1", canonicalFactID)
  ```
  *Or, insert if not present:*
  ```go
  db.Exec("INSERT INTO truth_scores (canonical_fact_id, score) VALUES ($1, $2) ON CONFLICT (canonical_fact_id) DO UPDATE SET score = truth_scores.score - 0.1, last_updated = NOW()", canonicalFactID, initialScoreAdjustment)
  ```

---

### **5. Local Testing and Debugging**

#### **5.1. Testing Fact Discovery and Categorization**
- **Unit Tests**: Write unit tests for your Go code to ensure that facts are correctly scraped, classified, and stored.
- **Mocking APIs**: Use mock responses when testing interactions with external APIs (Diffbot, OpenAI, etc.).

#### **5.2. Testing Fact Comparison and Contradiction Detection**
- **Unit Tests**: Test the vector comparison logic locally to ensure that contradictions are detected.
- **Database Testing**: Use Supabase locally to test your database queries and ensure the correctness of the search queries.

#### **5.3. Generating a Ground Truth Corpus for Duplication Detection**
- **Goal**: Create a synthetic dataset (`testdata/corpus/duplication_ground_truth.json`) to rigorously test the fact duplication detection mechanism based on embeddings.
- **Structure**: The corpus contains JSON objects with `id`, `text` (simulated 3-5 sentence article content), and `group_id`.
  - Texts sharing the same `group_id` represent the same core fact, deliberately paraphrased (using synonyms, different sentence structures, focus shifts).
  - Texts with unique `group_id` represent distinct facts.
- **Composition**: Includes approximately 20 groups of related texts (2-5 variants each) and 30 standalone, unrelated texts.
- **Testing Usage**: 
  1. Embed the `text` field of each entry in the corpus.
  2. For each embedding, perform a similarity search (e.g., nearest neighbors) against all other embeddings in the corpus.
  3. Evaluate the results based on the `group_id`: 
     - If a neighbor shares the same `group_id`, it's a **correct match** (True Positive).
     - If a neighbor has a different `group_id` but is identified as a match by the algorithm (above similarity threshold), it's a **false positive**.
     - Conversely, failing to match texts with the same `group_id` (below similarity threshold) results in **false negatives**.
  4. Calculate metrics like Precision, Recall, and F1-score to quantify the duplication detection performance against this ground truth.

---

### **6. Deployment to the host**

Once everything is working locally, follow these steps to deploy to **the host**:

1. **Deploy Backend to the host**:
   - Push your Go app to GitHub, and link it to the host for deployment.
   - the host will auto-detect your Go app and deploy it.

2. **Set Up Supabase on the host**:
   - Create a Supabase instance on the host.
   - Set up the database schema (facts, embeddings, etc.).
   - Use environment variables to securely manage API keys and credentials.

3. **Schedule Periodic Jobs**:
   - Use the host's cron job feature to schedule tasks for fetching new facts and detecting contradictions at regular intervals.

---

### **7. Final Testing and Maintenance**

Once deployed to the host, monitor the system's performance and ensure that the fact discovery, categorization