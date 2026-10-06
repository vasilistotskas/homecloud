package icloud

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func names(cookies []*http.Cookie) map[string]string {
	out := map[string]string{}
	for _, c := range cookies {
		out[c.Name] = c.Value
	}
	return out
}

func TestJarSurvivesSaveAndRestore(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	j, err := newJar(clock)
	if err != nil {
		t.Fatal(err)
	}
	setup := mustURL(t, "https://setup.icloud.com/setup/ws/1/accountLogin")
	j.SetCookies(setup, []*http.Cookie{
		{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "tok", Domain: ".icloud.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600},
		{Name: "host-only", Value: "h", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode},
	})
	j.SetCookies(mustURL(t, "https://idmsa.apple.com/appleauth/auth/signin/complete"), []*http.Cookie{
		{Name: "aasp", Value: "a", Domain: ".apple.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode},
	})

	restored, err := restoreJar(j.save(), clock)
	if err != nil {
		t.Fatal(err)
	}
	drive := mustURL(t, "https://p123-drivews.icloud.com/retrieveItemDetailsInFolders")
	got := names(restored.Cookies(drive))
	if got["X-APPLE-WEBAUTH-TOKEN"] != "tok" || got["host-only"] != "" || got["aasp"] != "" {
		t.Fatalf("cookies for a webservice host = %v; want only the .icloud.com domain cookie", got)
	}
	if names(restored.Cookies(setup))["host-only"] != "h" {
		t.Fatal("the host-only cookie was lost")
	}

	// Max-Age is stored as the absolute expiry it stood for when received.
	for _, r := range j.save() {
		c, err := http.ParseSetCookie(r.SetCookie)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name == "X-APPLE-WEBAUTH-TOKEN" && (!c.Expires.Equal(now.Add(time.Hour).Truncate(time.Second)) || c.MaxAge != 0) {
			t.Fatalf("stored token cookie expires %v (max-age %d); want %v", c.Expires, c.MaxAge, now.Add(time.Hour))
		}
	}
}

func TestJarForgetsDeletedCookies(t *testing.T) {
	j, err := newJar(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	u := mustURL(t, "https://setup.icloud.com/")
	j.SetCookies(u, []*http.Cookie{{Name: "a", Value: "1", Domain: ".icloud.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}})
	j.SetCookies(u, []*http.Cookie{{Name: "a", Value: "", Domain: ".icloud.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1}})
	if len(j.save()) != 0 || len(j.Cookies(u)) != 0 {
		t.Fatalf("a deleted cookie is still kept: %v", j.save())
	}
}

func TestJarKeepsOneRecordPerCookieAcrossRestores(t *testing.T) {
	j, err := newJar(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	u := mustURL(t, "https://setup.icloud.com/setup/ws/1/accountLogin")
	set := func(j *jar, value string) {
		j.SetCookies(u, []*http.Cookie{{Name: "X-APPLE-WEBAUTH-TOKEN", Value: value, Domain: ".icloud.com", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}})
	}
	set(j, "old")
	// Restore, then Apple renews the cookie, as each session use does.
	for _, value := range []string{"new", "newer"} {
		restored, restoreErr := restoreJar(j.save(), time.Now)
		if restoreErr != nil {
			t.Fatal(restoreErr)
		}
		set(restored, value)
		j = restored
	}
	if n := len(j.save()); n != 1 {
		t.Fatalf("%d records for one cookie: %v", n, j.save())
	}
	final, err := restoreJar(j.save(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(final.Cookies(mustURL(t, "https://p1-drivews.icloud.com/"))); got["X-APPLE-WEBAUTH-TOKEN"] != "newer" {
		t.Fatalf("restored token = %q; want the newest", got["X-APPLE-WEBAUTH-TOKEN"])
	}
}
