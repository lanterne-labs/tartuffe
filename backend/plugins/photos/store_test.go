package photos

import (
	"context"
	"os"
	"testing"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupStoreTest connects to the test database with a clean persons table.
func setupStoreTest(t *testing.T) (*storage.PostgresStorer, *Store) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping store tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)

	_, err = storer.Pool().Exec(ctx, "TRUNCATE TABLE persons RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	return storer, NewStore(storer)
}

func TestStore_SetPersonImageURL(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()

	withPhotoID, _, err := storer.AddPerson(ctx, "photo test with image")
	require.NoError(t, err)
	withoutPhotoID, _, err := storer.AddPerson(ctx, "photo test without image")
	require.NoError(t, err)

	persons, err := store.GetPersonsWithoutImage(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 2)
	assert.Nil(t, persons[0].ImageURL)
	assert.Nil(t, persons[1].ImageURL)

	const url = "https://example.com/photos/1.jpg"
	require.NoError(t, store.SetPersonImageURL(ctx, withPhotoID, url))

	persons, err = store.GetPersonsWithoutImage(ctx)
	require.NoError(t, err)
	require.Len(t, persons, 1)
	assert.Equal(t, withoutPhotoID, persons[0].ID)

	person, err := storer.GetPersonByID(ctx, withPhotoID)
	require.NoError(t, err)
	require.NotNil(t, person.ImageURL)
	assert.Equal(t, url, *person.ImageURL)
}
