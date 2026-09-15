package frpolitics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The scrutin's seanceRef is the key that pairs a vote with what its author
// said in the same sitting (the transcripts carry the same id); it must reach
// Law verbatim.
func TestParseScrutin_SeanceRef(t *testing.T) {
	var s scrutinJSON
	s.Scrutin.Numero = 4373
	s.Scrutin.Legislature = 17
	s.Scrutin.Titre = "le sous-amendement n° 372"
	s.Scrutin.DateScrutin = "2025-11-27"
	s.Scrutin.SeanceRef = " RUANR5L17S2026IDS29979 "
	law, _, err := ParseScrutin(s)
	require.NoError(t, err)
	require.Equal(t, "17-4373", law.Key)
	require.Equal(t, "RUANR5L17S2026IDS29979", law.SeanceRef)
}
