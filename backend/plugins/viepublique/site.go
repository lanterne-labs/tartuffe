package viepublique

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"digital-mirror/backend/webfetch"
)

// The site answers a client without its cookie with a page whose only
// script sends it to /redirect_<token>/<path>; that address sets the cookie
// (bot_mitigation_cookie) and redirects back to the page. The site then
// serves the page while the cookie lasts, and sends the script again when it
// expired.
func init() { webfetch.Register(&site{pace: webfetch.Pace{Rate: 2, Parallel: 2}}) }

type site struct {
	pace webfetch.Pace
}

// errChallenge marks a page still behind the check after the redirect step.
var errChallenge = errors.New("vie-publique: the page is still behind the bot check")

var redirectScript = regexp.MustCompile(`window\.location\.href\s*=\s*'(/redirect_[^']+)'`)

func (*site) Hosts() []string { return []string{"vie-publique.fr"} }

func (s *site) Fetch(ctx context.Context, c webfetch.Client, r webfetch.Request) (*webfetch.Page, error) {
	return webfetch.Paced(ctx, &s.pace, 3, func(ctx context.Context) (*webfetch.Page, error) {
		p, err := webfetch.Get(ctx, c, r)
		if err != nil {
			return p, err
		}
		m := redirectScript.FindSubmatch(p.Body)
		if m == nil {
			return p, nil
		}
		base, err := url.Parse(r.URL)
		if err != nil {
			return nil, fmt.Errorf("vie-publique: %w", err)
		}
		step, err := base.Parse(string(m[1]))
		if err != nil {
			return nil, fmt.Errorf("vie-publique: redirect %q: %w", m[1], err)
		}
		rr := r
		rr.URL = step.String()
		p, err = webfetch.Get(ctx, c, rr)
		if err != nil {
			return p, err
		}
		if redirectScript.Match(p.Body) {
			c.Note(ctx, r.URL, "challenge")
			return p, errChallenge
		}
		return p, nil
	})
}
