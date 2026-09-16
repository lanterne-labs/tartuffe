package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
)

// fuzzyFloor is the similarity a fuzzy name match must reach for a command
// to take it without asking.
const fuzzyFloor = 0.8

// stdin is shared by every prompt so typed-ahead input is not lost between
// prompts of one command.
var stdin = bufio.NewReader(os.Stdin)

func readStdinLine() (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// resolvePersonInteractive resolves name for a command that can ask the user:
// an exact match is taken as is, fuzzy candidates are offered as a numbered
// choice (0 skips), and when nothing matches and allowCreate is set the user
// may create the person (y) or party (p). ok is false when the user skipped.
func resolvePersonInteractive(ctx context.Context, storer *storage.PostgresStorer, name string, allowCreate bool) (person core.PersonRecord, ok bool, err error) {
	person, err = core.ResolvePersonID(ctx, storer, name, core.ExactMatchOnly)
	if err == nil {
		log.Info().Str("query", name).Str("matched", person.Name).Msg("Exact match found")
		return person, true, nil
	}
	var notFound *core.PersonNotFoundError
	if !errors.As(err, &notFound) {
		return core.PersonRecord{}, false, err
	}
	fmt.Printf("Query: %q\n", name)
	if len(notFound.Candidates) > 0 {
		return chooseCandidate(notFound.Candidates)
	}
	if !allowCreate {
		return core.PersonRecord{}, false, err
	}
	return promptCreatePerson(ctx, storer, name)
}

func chooseCandidate(candidates []core.MatchCandidate) (core.PersonRecord, bool, error) {
	fmt.Printf("Found %d potential matches:\n", len(candidates))
	for i, c := range candidates {
		fmt.Printf("  %d) %s (%s) - similarity: %.2f\n", i+1, c.Target.Name, c.Target.EntityType, c.Score)
	}
	fmt.Printf("  0) Skip\n")
	fmt.Printf("Enter your choice (0-%d): ", len(candidates))
	input, err := readStdinLine()
	if err != nil {
		return core.PersonRecord{}, false, err
	}
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 0 || choice > len(candidates) {
		return core.PersonRecord{}, false, fmt.Errorf("invalid choice %q", input)
	}
	if choice == 0 {
		fmt.Println("Skipped")
		return core.PersonRecord{}, false, nil
	}
	selected := candidates[choice-1].Target
	fmt.Printf("Selected: %s\n", selected.Name)
	return selected.Record(), true, nil
}

func promptCreatePerson(ctx context.Context, storer *storage.PostgresStorer, name string) (core.PersonRecord, bool, error) {
	fmt.Println("No matches found in database.")
	fmt.Print("Create this person/party? (y=person, p=party, n=skip): ")
	input, err := readStdinLine()
	if err != nil {
		return core.PersonRecord{}, false, err
	}
	var entityType string
	switch strings.ToLower(input) {
	case "y":
		entityType = "human"
	case "p":
		entityType = "party"
	default:
		fmt.Println("Skipped")
		return core.PersonRecord{}, false, nil
	}
	person, err := createPerson(ctx, storer, name, entityType)
	if err != nil {
		return core.PersonRecord{}, false, err
	}
	return person, true, nil
}

// createPerson adds name as entityType (idempotent) and reports the outcome.
func createPerson(ctx context.Context, storer *storage.PostgresStorer, name, entityType string) (core.PersonRecord, error) {
	id, inserted, err := storer.AddPersonWithType(ctx, name, entityType)
	if err != nil {
		return core.PersonRecord{}, fmt.Errorf("create %s %q: %w", entityType, name, err)
	}
	stored := util.NormalizePersonName(name)
	if inserted {
		log.Info().Str("name", stored).Int64("id", id).Str("type", entityType).Msg("Created new person")
		fmt.Printf("Created new %s: %s (ID: %d)\n", entityType, stored, id)
	} else {
		log.Info().Str("name", stored).Int64("id", id).Msg("Person already exists")
	}
	return core.PersonRecord{ID: id, Name: stored, EntityType: entityType}, nil
}

// resolveOrCreatePerson takes an exact or confident fuzzy match, and creates
// name as entityType when nobody matches (the --force path of ingest commands).
func resolveOrCreatePerson(ctx context.Context, storer *storage.PostgresStorer, name, entityType string) (core.PersonRecord, error) {
	person, err := core.ResolvePersonID(ctx, storer, name, fuzzyFloor)
	var notFound *core.PersonNotFoundError
	if errors.As(err, &notFound) {
		log.Info().Str("name", name).Str("type", entityType).Msg("Creating person (force mode)")
		return createPerson(ctx, storer, name, entityType)
	}
	return person, err
}

// personsForCommand returns the one person named by --person when set,
// otherwise every person listAll returns.
func personsForCommand(ctx context.Context, storer *storage.PostgresStorer, person string, listAll func(context.Context) ([]storage.PersonInfo, error)) ([]core.PersonRecord, error) {
	if person != "" {
		p, err := core.ResolvePersonID(ctx, storer, person, core.ExactMatchOnly)
		if err != nil {
			return nil, err
		}
		return []core.PersonRecord{p}, nil
	}
	all, err := listAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list persons: %w", err)
	}
	return toPersonRecords(all), nil
}

// resolvePersonsFromFile reads one person name per line and resolves each to
// an exact match; unresolved names are logged and skipped.
func resolvePersonsFromFile(ctx context.Context, storer *storage.PostgresStorer, path string) ([]core.PersonRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open persons-file: %w", err)
	}
	defer f.Close()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		names = append(names, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan persons-file: %w", err)
	}
	return core.ResolvePersons(ctx, storer, names, core.ExactMatchOnly)
}

func toPersonRecord(p storage.PersonInfo) core.PersonRecord {
	return core.PersonRecord{ID: p.ID, Name: p.Name, EntityType: p.EntityType}
}

func toPersonRecords(persons []storage.PersonInfo) []core.PersonRecord {
	out := make([]core.PersonRecord, len(persons))
	for i, p := range persons {
		out[i] = toPersonRecord(p)
	}
	return out
}

func toPersonInfo(p core.PersonRecord) storage.PersonInfo {
	return storage.PersonInfo{ID: p.ID, Name: p.Name, EntityType: p.EntityType}
}
