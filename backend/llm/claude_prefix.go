package llm

import "strings"

// minCachePrefixChars is the shortest prefix that gets a breakpoint. Claude
// caches a prefix of 512 tokens or more on the 5.5 models; a shorter one is
// not cached, and costs nothing more.
const minCachePrefixChars = 2000

// familyKeyBytes is the length of the prompt start that names a prompt
// family, with the task and the system text: the prompts of one template
// share it, so two templates under one task get their own prefix, and a
// cached prefix always has the same system text before it.
const familyKeyBytes = 256

// promptFamily returns the family of the prompt of r and the text of its
// first user message. ok is false when r has no single-block user message.
func promptFamily(task Task, r ClaudeRequest) (family, text string, ok bool) {
	_, text, ok = r.firstUserText()
	if !ok {
		return "", "", false
	}
	family = string(task) + "\x00" + joinBlocks(r.System) + "\x00" + text[:min(len(text), familyKeyBytes)]
	return family, text, true
}

// prefixCache holds the learned static prefixes of each prompt family. A
// family keeps its prefixes for the life of the endpoint: a cache entry
// matches only at the same position. The caller holds the lock of the
// endpoint.
type prefixCache struct {
	prefixes map[string][]string // prompt family -> learned prefixes
}

// known returns the longest learned prefix of family that starts text, or "".
func (c *prefixCache) known(family, text string) string {
	best := ""
	for _, p := range c.prefixes[family] {
		if len(p) > len(best) && strings.HasPrefix(text, p) {
			best = p
		}
	}
	return best
}

// learn gets the longest common start of texts, cut back to a line end. When
// it has minCachePrefixChars or more, learn keeps it as a prefix of family
// and returns it. Otherwise learn returns "".
func (c *prefixCache) learn(family string, texts []string) string {
	// The cut at a line end also keeps a multi-byte character whole.
	p := commonPrefix(texts)
	p = p[:strings.LastIndexByte(p, '\n')+1]
	if len(p) < minCachePrefixChars {
		return ""
	}
	if c.prefixes == nil {
		c.prefixes = map[string][]string{}
	}
	c.prefixes[family] = append(c.prefixes[family], p)
	return p
}

// commonPrefix returns the longest common start of the texts.
func commonPrefix(texts []string) string {
	p := texts[0]
	for _, s := range texts[1:] {
		n := 0
		for n < len(p) && n < len(s) && p[n] == s[n] {
			n++
		}
		p = p[:n]
	}
	return p
}
