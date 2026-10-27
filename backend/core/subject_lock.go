package core

import (
	"strings"
	"sync"

	"digital-mirror/backend/util"
)

// subjectLocks serializes the insertion of the facts of one subject across
// the process. Two facts of one subject inserted at once could both open a
// stance on one subject, or miss each other as a same claim; the collection
// tasks that run at once can carry facts of one person (an article found for
// one names the other).
var subjectLocks = keyedMutex{locks: map[string]*keyedLock{}}

// keyedMutex is a mutex per key; a key's mutex lives while someone holds or
// waits for it.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// Lock takes the mutex of key and returns the function that releases it.
func (k *keyedMutex) Lock(key string) (unlock func()) {
	k.mu.Lock()
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// subjectKey is the key of a subject name: the spellings of one person
// ("Édouard Philippe", "edouard philippe") share it.
func subjectKey(subject string) string {
	if key := util.PersonNameKey(subject); key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(subject))
}
