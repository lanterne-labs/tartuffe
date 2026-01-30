package main

import (
	"digital-mirror/cmd"
	// "fmt"
	// "log"
	// "digital-mirror/backend"
	// "digital-mirror/backend/categorizer"
	// "digital-mirror/backend/embedding"
	// "digital-mirror/backend/extractor"
	// "digital-mirror/backend/storage"
	// "github.com/joho/godotenv"
)

func main() {
	cmd.Execute()
}

/*
// --- Old main function - kept for reference, will be replaced by CLI ---
func old_main() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Println("INFO: .env file not found, relying on environment variables.")
	}

	// --- Initialize Components ---
	ctx := context.Background()

	// Storage (Postgres)
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize database storer: %v", err)
	}
	defer storer.Close()
	log.Println("Database connection successful.")

	// Content Extractor (Apify)
	contentExtractor, err := extractor.NewApifyExtractorFromEnv()
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Apify content extractor: %v", err)
	}
	log.Println("Content extractor initialized (Apify).")

    // Fact Extractor (OpenAI-compatible)
    factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Mistral fact extractor: %v", err)
	}
	log.Println("Fact extractor initialized (Mistral).")

    // Embedding Generator (OpenAI-compatible)
    embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Mistral embedder: %v", err)
	}
	log.Println("Embedding generator initialized (Mistral).")

	// --- Pipeline Logic (Example) ---
	targetPerson := "Gabriel Attal" // Example target
	// TODO: Replace with actual search logic
	urlsToProcess := []string{
		"https://www.bfmtv.com/politique/gouvernement/remaniement-en-direct-le-gouvernement-de-gabriel-attal-se-precise-les-annonces-attendues-ce-jeudi_LN-202401110113.html",
		"https://www.lemonde.fr/politique/article/2024/01/09/gabriel-attal-nomme-premier-ministre-plus-jeune-chef-du-gouvernement-et-premier-ouvertement-homosexuel_6210007_823448.html",
		"https://www.example.com/already-processed", // Add a dummy URL to test skipping
	}

	log.Printf("Starting pipeline for person: %s", targetPerson)

	for _, url := range urlsToProcess {
		log.Printf("Processing URL: %s", url)

		// 1. Check if URL already processed
		exists, err := storer.CheckSourceURLExists(ctx, url)
		if err != nil {
			log.Printf("ERROR checking URL existence for %s: %v. Skipping.", url, err)
			continue
		}
		if exists {
			log.Printf("INFO: URL %s already processed. Skipping.", url)
			continue
		}

		// 2. Extract Content
		content, err := contentExtractor.ExtractContent(url)
		if err != nil {
			log.Printf("ERROR extracting content from %s: %v. Skipping.", url, err)
			continue
		}
		if content == nil || content.Content == "" {
			log.Printf("WARN: No content extracted from %s. Skipping.", url)
			continue
		}
		log.Printf("Content extracted from %s", url)

		// 3. Extract Facts
		facts, err := factExtractor.ExtractFacts(targetPerson, *content)
		if err != nil {
			log.Printf("ERROR extracting facts from %s: %v. Skipping URL.", url, err)
			continue
		}
		if len(facts) == 0 {
			log.Printf("INFO: No facts extracted from %s.", url)
			continue
		}
		log.Printf("Extracted %d facts from %s", len(facts), url)

		// 4. Process Each Fact
		for i, fact := range facts {
			log.Printf("  Processing fact %d/%d: '%s'", i+1, len(facts), fact.NormalizedFactText)

			// 4a. Generate Embedding from EmbeddingText (name-free version) to cluster by topic
			embeddings, err := embedder.GenerateEmbeddings(ctx, []string{fact.EmbeddingText})
			if err != nil {
				log.Printf("  ERROR generating embedding: %v. Skipping fact.", err)
				continue
			}
			if len(embeddings) == 0 || len(embeddings[0]) == 0 {
				log.Printf("  ERROR: Embedder returned empty embedding. Skipping fact.")
				continue
			}

			// 4b. Prepare for Storage
			storeInput := storage.StoreFactInput{
				NormalizedFactText: fact.NormalizedFactText,
				Embedding:          embeddings[0],
				OriginalQuote:      fact.OriginalQuote,
				Topics:             fact.Topics,
				InvolvedPeople:     fact.InvolvedPeople,
				SourceURL:          url,
				PublishedAt:        content.DatePublished, // Use date from content extraction
			}

			// 4c. Store Fact
			factID, factSourceID, err := storer.StoreFact(ctx, storeInput)
			if err != nil {
				log.Printf("  ERROR storing fact (NormText: '%s', URL: %s): %v", fact.NormalizedFactText, url, err)
				// Continue processing other facts from this URL
			} else {
				log.Printf("  Successfully stored/linked fact (FactID: %d, SourceID: %d)", factID, factSourceID)
			}
		}
	}

	log.Println("Pipeline finished for person: ", targetPerson)
}
*/
