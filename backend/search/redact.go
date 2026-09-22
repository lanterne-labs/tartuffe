package search

import (
	"fmt"
	"regexp"
)

// SerpAPI takes its key as a query parameter, and net/http quotes the whole
// URL in transport errors (*url.Error), so an unwrapped failure prints the
// key verbatim into logs and CI output. Every error that may carry a URL is
// passed through redactSecrets.
var secretParamRe = regexp.MustCompile(`(?i)([?&](?:api_?key|key|token|secret|password)=)[^&\s"']+`)

// redactSecrets replaces secret query-parameter values in an error's message.
// The returned error keeps the message only — the chain is dropped on
// purpose, because errors.As on a *url.Error would hand the caller the
// unredacted URL again.
func redactSecrets(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", secretParamRe.ReplaceAllString(err.Error(), "${1}REDACTED"))
}
