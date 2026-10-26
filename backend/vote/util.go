package vote

import (
	"encoding/hex"
	"errors"

	"digital-mirror/backend/vote/grumpkin"
)

func hexOf(b []byte) string { return hex.EncodeToString(b) }

// pointOf builds a point from its hex coordinates and checks it.
func pointOf(x, y string) (grumpkin.Point, error) {
	var p grumpkin.Point
	xe, err := parseField(x)
	if err != nil {
		return p, err
	}
	ye, err := parseField(y)
	if err != nil {
		return p, err
	}
	p.X, p.Y = xe, ye
	if !p.IsOnCurve() {
		return p, errNotOnCurve
	}
	return p, nil
}

var errNotOnCurve = errors.New("not on the curve")
