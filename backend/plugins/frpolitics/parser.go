package frpolitics

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IntOrString unmarshals a JSON field that may be a number or a quoted string number
type IntOrString int

func (i *IntOrString) UnmarshalJSON(b []byte) error {
	// Try numeric first
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		if iv, err2 := strconv.Atoi(n.String()); err2 == nil {
			*i = IntOrString(iv)
			return nil
		}
	}
	// Try plain string
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if iv, err2 := strconv.Atoi(s); err2 == nil {
			*i = IntOrString(iv)
			return nil
		}
	}
	// Fallback: zero
	*i = 0
	return nil
}

// Minimal in-memory representations of parts of the scrutin JSON
type scrutinJSON struct {
	Scrutin struct {
		Numero           IntOrString `json:"numero"`
		Titre            string      `json:"titre"`
		DateScrutin      string      `json:"dateScrutin"`
		Legislature      IntOrString `json:"legislature"`
		UrlDossier       string      `json:"urlDossier"`
		VentilationVotes struct {
			Organe struct {
				OrganeRef string `json:"organeRef"`
				Groupes   struct {
					Groupe []struct {
						OrganeRef           string `json:"organeRef"`
						NombreMembresGroupe string `json:"nombreMembresGroupe"`
						Vote                struct {
							PositionMajoritaire string `json:"positionMajoritaire"`
							DecompteVoix        struct {
								Pour        string `json:"pour"`
								Contre      string `json:"contre"`
								Abstentions string `json:"abstentions"`
								NonVotants  string `json:"nonVotants"`
							} `json:"decompteVoix"`
							DecompteNominatif struct {
								NonVotants struct {
									Votant any `json:"votant"`
								} `json:"nonVotants"`
								Pours struct {
									Votant any `json:"votant"`
								} `json:"pours"`
								Contres struct {
									Votant any `json:"votant"`
								} `json:"contres"`
								Abstentions struct {
									Votant any `json:"votant"`
								} `json:"abstentions"`
							} `json:"decompteNominatif"`
						} `json:"vote"`
					} `json:"groupe"`
				} `json:"groupes"`
			} `json:"organe"`
		} `json:"ventilationVotes"`
		DecompteNominatif struct {
			// The exact structure differs across datasets; we support common forms
			// Positions with arrays of voters: pour/contre/abstentions/nonVotants
			Pour struct {
				Votant []nominativeVoter `json:"votant"`
			} `json:"pour"`
			Contre struct {
				Votant []nominativeVoter `json:"votant"`
			} `json:"contre"`
			Abstentions struct {
				Votant []nominativeVoter `json:"votant"`
			} `json:"abstentions"`
			NonVotants struct {
				Votant []nominativeVoter `json:"votant"`
			} `json:"nonVotants"`
		} `json:"decompteNominatif"`
	} `json:"scrutin"`
}

// nominativeVoter models a single nominative vote entry
type nominativeVoter struct {
	ActeurRef string `json:"acteurRef"`
	OrganeRef string `json:"organeRef"`
	Nom       string `json:"nom"`
}

// ParseScrutin converts raw scrutinJSON into Law and a map from party organeRef to majority position and counts
func ParseScrutin(s scrutinJSON) (Law, LawGroupStats, error) {
	lawKey := fmt.Sprintf("%d-%d", s.Scrutin.Legislature, int(s.Scrutin.Numero))
	var voteTime *time.Time
	if s.Scrutin.DateScrutin != "" {
		// Try RFC3339 first, then date-only format
		if t, err := time.Parse(time.RFC3339, s.Scrutin.DateScrutin); err == nil {
			voteTime = &t
		} else if t, err := time.Parse("2006-01-02", s.Scrutin.DateScrutin); err == nil {
			voteTime = &t
		}
	}
	law := Law{
		Key:         lawKey,
		Title:       s.Scrutin.Titre,
		URL:         s.Scrutin.UrlDossier,
		Legislature: int(s.Scrutin.Legislature),
		VoteDate:    voteTime,
	}
	// Fallback URL if missing: build a valid, canonical-ish scrutin link
	if strings.TrimSpace(law.URL) == "" {
		law.URL = fmt.Sprintf("https://www.assemblee-nationale.fr/dyn/%d/scrutins/%d", law.Legislature, int(s.Scrutin.Numero))
	}

	groups := make(LawGroupStats)
	for _, g := range s.Scrutin.VentilationVotes.Organe.Groupes.Groupe {
		pos := VotePosition(g.Vote.PositionMajoritaire)
		// best-effort parse ints
		toInt := func(x string) int { var n int; fmt.Sscanf(x, "%d", &n); return n }
		groups[g.OrganeRef] = GroupStats{
			Position: pos,
			For:      toInt(g.Vote.DecompteVoix.Pour),
			Against:  toInt(g.Vote.DecompteVoix.Contre),
			Abst:     toInt(g.Vote.DecompteVoix.Abstentions),
			NonVot:   toInt(g.Vote.DecompteVoix.NonVotants),
		}
	}
	return law, groups, nil
}

// ParseNominatif extracts per-deputy VoteEvent list from a scrutin JSON.
// It tolerates missing organeRef and name; those can be resolved by separate actor/organe datasets.
func ParseNominatif(s scrutinJSON) []VoteEvent {
	var out []VoteEvent
	// Prefer deep path under ventilationVotes when present, which may differ in shape
	for _, g := range s.Scrutin.VentilationVotes.Organe.Groupes.Groupe {
		orgRef := g.OrganeRef
		// helper to normalize any -> []map[string]string
		toSlice := func(v any) []map[string]any {
			switch vv := v.(type) {
			case nil:
				return nil
			case map[string]any:
				return []map[string]any{vv}
			case []any:
				out := make([]map[string]any, 0, len(vv))
				for _, e := range vv {
					if m, ok := e.(map[string]any); ok {
						out = append(out, m)
					}
				}
				return out
			default:
				return nil
			}
		}
		dn := g.Vote.DecompteNominatif
		for _, m := range toSlice(dn.Pours.Votant) {
			out = append(out, VoteEvent{AssembleeID: asString(m["acteurRef"]), OrganeRef: orgRef, Position: VoteFor})
		}
		for _, m := range toSlice(dn.Contres.Votant) {
			out = append(out, VoteEvent{AssembleeID: asString(m["acteurRef"]), OrganeRef: orgRef, Position: VoteAgainst})
		}
		for _, m := range toSlice(dn.Abstentions.Votant) {
			out = append(out, VoteEvent{AssembleeID: asString(m["acteurRef"]), OrganeRef: orgRef, Position: VoteAbstention})
		}
		for _, m := range toSlice(dn.NonVotants.Votant) {
			out = append(out, VoteEvent{AssembleeID: asString(m["acteurRef"]), OrganeRef: orgRef, Position: VoteNonVotant})
		}
	}
	// Fallback simple structure
	appendBlock := func(voters []nominativeVoter, pos VotePosition) {
		for _, v := range voters {
			out = append(out, VoteEvent{AssembleeID: v.ActeurRef, OrganeRef: v.OrganeRef, DisplayName: v.Nom, Position: pos})
		}
	}
	appendBlock(s.Scrutin.DecompteNominatif.Pour.Votant, VoteFor)
	appendBlock(s.Scrutin.DecompteNominatif.Contre.Votant, VoteAgainst)
	appendBlock(s.Scrutin.DecompteNominatif.Abstentions.Votant, VoteAbstention)
	appendBlock(s.Scrutin.DecompteNominatif.NonVotants.Votant, VoteNonVotant)
	return out
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
