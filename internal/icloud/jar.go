package icloud

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// jar is an http.CookieJar that can be saved and restored. Matching is the
// standard library's (RFC 6265, with the public suffix list); jar records each
// cookie as it was set, with the URL that set it, and replays those records
// into a fresh cookiejar.Jar on restore, so expiry and domain rules apply
// exactly as they did when the cookies arrived.
type jar struct {
	mu      sync.Mutex
	inner   *cookiejar.Jar
	records map[string]cookieRecord
	now     func() time.Time
}

// cookieRecord is one Set-Cookie as received. A relative Max-Age is stored as
// an absolute Expires so that a restored cookie expires when it would have.
type cookieRecord struct {
	URL       string `json:"url"`
	SetCookie string `json:"set_cookie"`
}

func newJar(now func() time.Time) (*jar, error) {
	inner, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	return &jar{inner: inner, records: map[string]cookieRecord{}, now: now}, nil
}

// restoreJar rebuilds a jar from saved records. Records that expired since are
// dropped by the standard jar as they are replayed.
func restoreJar(records []cookieRecord, now func() time.Time) (*jar, error) {
	j, err := newJar(now)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		u, err := url.Parse(r.URL)
		if err != nil {
			return nil, fmt.Errorf("saved cookie URL %q: %w", r.URL, err)
		}
		c, err := http.ParseSetCookie(r.SetCookie)
		if err != nil {
			return nil, fmt.Errorf("saved cookie for %s: %w", u.Host, err)
		}
		j.SetCookies(u, []*http.Cookie{c})
	}
	return j, nil
}

// SetCookies implements http.CookieJar.
func (j *jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.inner.SetCookies(u, cookies)
	for _, c := range cookies {
		scope := c.Domain
		if scope == "" {
			scope = u.Host
		}
		key := scope + "|" + c.Path + "|" + c.Name
		if c.MaxAge < 0 || (!c.Expires.IsZero() && !c.Expires.After(j.now())) {
			delete(j.records, key)
			continue
		}
		stored := *c //nolint:gosec // a cookie Apple set, recorded as received; homecloud issues no cookies here
		if stored.MaxAge > 0 {
			stored.Expires = j.now().Add(time.Duration(stored.MaxAge) * time.Second)
			stored.MaxAge = 0
		}
		j.records[key] = cookieRecord{URL: (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String(), SetCookie: stored.String()}
	}
}

// Cookies implements http.CookieJar.
func (j *jar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.inner.Cookies(u)
}

// save returns the records to persist.
func (j *jar) save() []cookieRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]cookieRecord, 0, len(j.records))
	for _, r := range j.records {
		out = append(out, r)
	}
	return out
}
