package httpgateway

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxSessionCookies = 128
const maxSessionCookieBytes = 64 << 10

type cookieRecord struct {
	size    int
	expires time.Time
}

// boundedCookieJar keeps the standard jar's cookie matching and expiry rules,
// while placing a finite bound on session state even for a noisy upstream.
type boundedCookieJar struct {
	mu       sync.Mutex
	jar      *cookiejar.Jar
	records  map[string]cookieRecord
	bytes    int
	overflow bool
}

func newCookieJar() *boundedCookieJar {
	jar, _ := cookiejar.New(nil)
	return &boundedCookieJar{jar: jar, records: map[string]cookieRecord{}}
}
func (j *boundedCookieJar) Cookies(u *url.URL) []*http.Cookie { return j.jar.Cookies(u) }
func (j *boundedCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	for key, record := range j.records {
		if !record.expires.IsZero() && !now.Before(record.expires) {
			j.bytes -= record.size
			delete(j.records, key)
		}
	}
	accepted := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
		if domain == "" {
			domain = u.Hostname()
		}
		path := cookie.Path
		if !strings.HasPrefix(path, "/") {
			path = u.Path
			if last := strings.LastIndex(path, "/"); last > 0 {
				path = path[:last]
			} else {
				path = "/"
			}
		}
		key := domain + "\x00" + path + "\x00" + cookie.Name
		previous := j.records[key]
		if cookie.MaxAge < 0 || !cookie.Expires.IsZero() && !now.Before(cookie.Expires) {
			j.bytes -= previous.size
			delete(j.records, key)
			accepted = append(accepted, cookie)
			continue
		}
		size := len(key) + len(cookie.String())
		if j.bytes-previous.size+size > maxSessionCookieBytes || previous.size == 0 && len(j.records) >= maxSessionCookies {
			j.overflow = true
			continue
		}
		expires := cookie.Expires
		if cookie.MaxAge > 0 {
			expires = now.Add(time.Duration(cookie.MaxAge) * time.Second)
		}
		j.records[key] = cookieRecord{size, expires}
		j.bytes += size
		accepted = append(accepted, cookie)
	}
	j.jar.SetCookies(u, accepted)
}
func (j *boundedCookieJar) exceeded() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.overflow }
