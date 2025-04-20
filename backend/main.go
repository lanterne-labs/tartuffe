package main

import (
	"fmt"
	"log"

	"github.com/joho/godotenv"
)

func main() {
	fmt.Println("Digital Mirror Backend Starting...")

	// Load environment variables from .env file if present
	err := godotenv.Load()
	if err != nil {
		log.Println("INFO: No .env file found or error loading it.")
	} else {
		log.Println("INFO: Loaded environment variables from .env file.")
	}

	// TODO: Set up actual backend server/service entry point here
	// For now, it just loads env and exits.

	fmt.Println("Digital Mirror Backend finished.")
} 