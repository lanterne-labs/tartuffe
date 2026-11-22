package extractor

import "errors"

// ErrQuota marks a page a reader did not read because its account is out of
// credit (HTTP 402). The page may well be readable: the caller keeps it to
// try again instead of counting it as a page no reader can read.
var ErrQuota = errors.New("page reader out of credit")
