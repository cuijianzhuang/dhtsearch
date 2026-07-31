// Package admin serves the operator console: a dashboard over the pipeline
// counters, live configuration, and the handful of write operations that
// otherwise need a SQL prompt on the server.
//
// Everything here is same-origin by construction. The public API answers with
// Access-Control-Allow-Origin: *, and a wildcard cannot be combined with
// credentialed requests — a browser refuses to send cookies to it. Serving the
// console and its API from this one Go process, behind the site's own origin,
// sidesteps that entirely: no CORS, no cross-origin cookie rules, and nothing
// about the console ends up in the public frontend bundle.
package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie = "dhtsearch_admin"
	csrfCookie    = "dhtsearch_admin_csrf"
	csrfHeader    = "X-Admin-CSRF"

	sessionTTL = 12 * time.Hour
	// Sessions are held in memory, so a restart logs everyone out. That is the
	// right trade for a single-operator console: no session table to keep, and
	// a compromised token cannot outlive the process.
	maxSessions = 32

	// Login throttling. A single shared password is exactly the thing worth
	// guessing, and the console sits on the public internet, so failures are
	// counted per client and the lockout is long enough to make an online
	// guessing run pointless.
	maxLoginFails = 5
	lockoutWindow = 15 * time.Minute
)

// session is one logged-in browser.
type session struct {
	csrf    string
	expires time.Time
}

// auth holds the console's sessions and login throttling.
type auth struct {
	password string
	secure   bool // mark cookies Secure (site is served over HTTPS)

	mu       sync.Mutex
	sessions map[string]session
	fails    map[string]*failCount

	now func() time.Time // injectable for tests
}

type failCount struct {
	n     int
	until time.Time
}

func newAuth(password string, secure bool) *auth {
	return &auth{
		password: password,
		secure:   secure,
		sessions: map[string]session{},
		fails:    map[string]*failCount{},
		now:      time.Now,
	}
}

// randomToken returns a URL-safe 256-bit token.
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the system has no entropy source. Returning
		// a predictable token would be worse than not starting a session.
		panic("admin: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// lockedOut reports whether this client must wait before trying again.
func (a *auth) lockedOut(key string) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.fails[key]
	if !ok {
		return 0, false
	}
	if d := f.until.Sub(a.now()); f.n >= maxLoginFails && d > 0 {
		return d, true
	}
	return 0, false
}

// checkPassword compares in constant time so a network observer cannot learn
// the password one byte at a time from response timing.
func (a *auth) checkPassword(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.password)) == 1
}

// noteFailure records a bad password attempt and extends the lockout.
func (a *auth) noteFailure(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[key]
	if f == nil {
		f = &failCount{}
		a.fails[key] = f
	}
	// Forget a stale streak so an honest operator who mistyped once last week
	// is not one slip away from a lockout.
	if a.now().After(f.until) && f.n >= maxLoginFails {
		f.n = 0
	}
	f.n++
	f.until = a.now().Add(lockoutWindow)
	if len(a.fails) > 4096 {
		a.sweepFails()
	}
}

// sweepFails drops expired throttling entries. Caller holds the lock.
func (a *auth) sweepFails() {
	now := a.now()
	for k, f := range a.fails {
		if now.After(f.until) {
			delete(a.fails, k)
		}
	}
}

// startSession issues a session and its paired CSRF token.
func (a *auth) startSession(key string) (token, csrf string) {
	token, csrf = randomToken(), randomToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, key)
	a.sweepSessions()
	if len(a.sessions) >= maxSessions {
		// Full of live sessions: drop an arbitrary one rather than refuse the
		// operator a login. Worst case someone else's tab has to sign in again.
		for k := range a.sessions {
			delete(a.sessions, k)
			break
		}
	}
	a.sessions[token] = session{csrf: csrf, expires: a.now().Add(sessionTTL)}
	return token, csrf
}

// sweepSessions drops expired sessions. Caller holds the lock.
func (a *auth) sweepSessions() {
	now := a.now()
	for k, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, k)
		}
	}
}

// lookup returns the live session for a token.
func (a *auth) lookup(token string) (session, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[token]
	if !ok || a.now().After(s.expires) {
		return session{}, false
	}
	return s, true
}

func (a *auth) endSession(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// setCookies writes the session and CSRF cookies.
//
// The session cookie is HttpOnly so script cannot read it, and SameSite=Strict
// so it never rides along with a cross-site request — which is the primary
// CSRF defence. The CSRF cookie is deliberately readable by script: the page
// echoes it back in a header, and only same-origin script can do that.
func (a *auth) setCookies(w http.ResponseWriter, token, csrf string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/",
		HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func (a *auth) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/",
			HttpOnly: name == sessionCookie, Secure: a.secure,
			SameSite: http.SameSiteStrictMode, MaxAge: -1,
		})
	}
}

// authorize resolves a request to its session, enforcing the CSRF check on
// anything that is not a read.
//
// The header check is belt and braces over SameSite=Strict: a cross-site form
// post cannot set a custom header, and a browser that ignores SameSite still
// cannot forge one. Reads are exempt because they change nothing and the
// dashboard fetches them on load.
func (a *auth) authorize(r *http.Request, mutating bool) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	s, ok := a.lookup(c.Value)
	if !ok {
		return "", false
	}
	if mutating {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(s.csrf)) != 1 {
			return "", false
		}
	}
	return c.Value, true
}
