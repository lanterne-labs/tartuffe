// One-off maintenance tool: restore a contradiction the auditor wrongly evicted.
// Usage: DATABASE_URL=... go run ./tools/restore_contradiction <personID> <factA> <factB> "<reason FR>"
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"digital-mirror/backend/storage"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: restore_contradiction <personID> <factA> <factB> <reason>")
		os.Exit(1)
	}
	personID, _ := strconv.ParseInt(os.Args[1], 10, 64)
	factA, _ := strconv.ParseInt(os.Args[2], 10, 64)
	factB, _ := strconv.ParseInt(os.Args[3], 10, 64)
	reason := os.Args[4]

	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "storer: %v\n", err)
		os.Exit(1)
	}
	defer storer.Close()

	id, needsBundler, err := storer.RecordContradictionImmediate(ctx, personID, factA, factB, reason, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "record: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("restored contradiction id=%d needsBundler=%v\n", id, needsBundler)
}
