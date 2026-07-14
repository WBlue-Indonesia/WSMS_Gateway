package admin

import (
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/secret"
)

// A flash is a one-time, server-side stash for a freshly-minted secret. We never put
// the plaintext secret in the redirect URL (it would leak into browser history, proxy
// logs, and the Referer header). Instead we hand the browser only a random, single-use
// lookup id; the value is read exactly once, from memory, on the next page render.
type flashItem struct {
	kind    string // "apikey" | "webhook" | "signing"
	label   string // human hint, e.g. the client/key name
	secret  string // the plaintext, shown once
	expires time.Time
}

const flashTTL = 3 * time.Minute

// putFlash stores a secret and returns an opaque one-time id to carry in the redirect.
func (s *Server) putFlash(kind, label, plain string) string {
	id, _ := secret.RandomToken(16)
	s.flashMu.Lock()
	// opportunistic sweep of anything expired so the map can't grow unbounded
	now := time.Now()
	for k, v := range s.flashes {
		if now.After(v.expires) {
			delete(s.flashes, k)
		}
	}
	s.flashes[id] = flashItem{kind: kind, label: label, secret: plain, expires: now.Add(flashTTL)}
	s.flashMu.Unlock()
	return id
}

// popFlash reads and removes a flash by id. Returns ok=false if missing or expired.
func (s *Server) popFlash(id string) (flashItem, bool) {
	if id == "" {
		return flashItem{}, false
	}
	s.flashMu.Lock()
	it, ok := s.flashes[id]
	if ok {
		delete(s.flashes, id)
	}
	s.flashMu.Unlock()
	if !ok || time.Now().After(it.expires) {
		return flashItem{}, false
	}
	return it, true
}
