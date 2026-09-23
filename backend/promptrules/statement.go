package promptrules

import (
	"fmt"
	"strings"
)

// Statement is one side of a judged pair: the statement's text and, when it
// stands for a group of restatements, the other wordings of the same claim.
// Every contradiction judge and veto gate renders it with Render, so a gate
// that can drop a contradiction never sees less than the gate that found it.
type Statement struct {
	Text string
	// Also holds the other wordings of the same claim (group members), most
	// concrete first. They carry the figures, dates and targets a group's
	// headline text may leave out.
	Also []string
}

// Render is the statement as a prompt shows it: the quoted text, then one
// line of the other wordings when there are any.
func (s Statement) Render() string {
	out := fmt.Sprintf("%q", s.Text)
	if len(s.Also) == 0 {
		return out
	}
	quoted := make([]string, len(s.Also))
	for i, a := range s.Also {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return out + "\n  Other wordings of the same statement: " + strings.Join(quoted, " / ")
}

// RestatementsRule tells a judge how to read "Other wordings of the same
// statement". It sits in the rules part of each prompt (before the data), so
// prefix caching keeps working.
const RestatementsRule = `**RESTATEMENTS:** A statement may be followed by "Other wordings of the same statement". Those are the SAME person making the SAME claim, as reported by other sources; they carry details (figures, dates, targets, conditions) the first wording may leave out. Judge the statement with all of its wordings together: a detail present in any wording is part of the statement. NEVER compare the wordings of one statement with each other — differences between them are reporting variance, not a contradiction.`
