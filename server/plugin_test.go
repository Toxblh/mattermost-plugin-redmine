package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

type fakeAPI struct {
	plugin.API
	mu   sync.Mutex
	data map[string][]byte
}

func (f *fakeAPI) KVGet(k string) ([]byte, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.data[k]...), nil
}
func (f *fakeAPI) put(user string, c connection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(c)
	f.data[storageKey(user)] = b
}
func TestURLAndEncryptionBoundaries(t *testing.T) {
	for _, u := range []string{"http://redmine.test", "https://user:password@redmine.test", "https://redmine.test?token=x", "https://redmine.test/#x", "file:///etc/passwd"} {
		if _, e := parseBase(u); e == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	if _, e := parseBase("https://redmine.test/tracker/"); e != nil {
		t.Fatal(e)
	}
	c := config{EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	encrypted, e := seal(c, "alice", "https://redmine.test", "secret")
	if e != nil {
		t.Fatal(e)
	}
	conn := connection{Ciphertext: encrypted, Instance: "https://redmine.test"}
	if value, e := unseal(c, "alice", conn); e != nil || value != "secret" {
		t.Fatal("roundtrip failed")
	}
	if _, e = unseal(c, "bob", conn); e == nil {
		t.Fatal("another user decrypted key")
	}
	conn.Instance = "https://other.test"
	if _, e = unseal(c, "alice", conn); e == nil {
		t.Fatal("another instance decrypted key")
	}
}
func TestPersonalCacheAndSingleflight(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Redmine-API-Key") == "" || r.URL.RawQuery != "" {
			t.Error("credential header missing or query set")
		}
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"Private issue","description":"Details","status":{"name":"Open"}}}`))
	}))
	defer s.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = s.Client().Transport
	defer func() { http.DefaultTransport = transport }()
	c := config{EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), RequestTimeoutSeconds: 5}
	u, _ := parseBase(s.URL)
	api := &fakeAPI{data: map[string][]byte{}}
	p := &Plugin{}
	p.API = api
	_ = p.OnActivate()
	makeConn := func(user, rev string) connection {
		key, e := seal(c, user, s.URL, "secret-"+user)
		if e != nil {
			t.Fatal(e)
		}
		conn := connection{Revision: rev, Ciphertext: key, Instance: s.URL}
		api.put(user, conn)
		return conn
	}
	alice := makeConn("alice", "a1")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := p.getCard("alice", "42", alice, c, u, false)
			if e != nil || v.Title != "Private issue" {
				t.Errorf("card: %+v, %v", v, e)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls = %d", calls.Load())
	}
	first, _ := p.getCard("alice", "42", alice, c, u, false)
	second, _ := p.getCard("alice", "42", alice, c, u, false)
	if first.SourceFetchedAt != second.SourceFetchedAt || first.ExpiresAt != second.ExpiresAt {
		t.Fatal("cache hit extended freshness")
	}
	if first.ExpiresAt-first.SourceFetchedAt != 300000 {
		t.Fatal("wrong client TTL")
	}
	bob := makeConn("bob", "b1")
	if _, e := p.getCard("bob", "42", bob, c, u, false); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal("users shared cache")
	}
	p.mu.Lock()
	for key, value := range p.cache {
		if strings.HasPrefix(key, "alice|") {
			value.Until = time.Now().Add(-time.Second)
			p.cache[key] = value
		}
	}
	p.mu.Unlock()
	if _, e := p.getCard("alice", "42", alice, c, u, false); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 3 {
		t.Fatal("expired cache hit")
	}
	rotated := makeConn("alice", "a2")
	if _, e := p.getCard("alice", "42", rotated, c, u, false); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 4 {
		t.Fatal("revision reused old cache")
	}
}
func TestRevocationDuringFlight(t *testing.T) {
	start, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(start)
		<-release
		_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"Secret"}}`))
	}))
	defer s.Close()
	old := http.DefaultTransport
	http.DefaultTransport = s.Client().Transport
	defer func() { http.DefaultTransport = old }()
	c := config{EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), RequestTimeoutSeconds: 5}
	u, _ := parseBase(s.URL)
	api := &fakeAPI{data: map[string][]byte{}}
	p := &Plugin{}
	p.API = api
	_ = p.OnActivate()
	encrypted, _ := seal(c, "alice", s.URL, "key")
	conn := connection{Revision: "old", Ciphertext: encrypted, Instance: s.URL}
	api.put("alice", conn)
	done := make(chan error)
	go func() { _, e := p.getCard("alice", "42", conn, c, u, false); done <- e }()
	<-start
	api.put("alice", connection{Revision: "disconnected", Instance: s.URL})
	close(release)
	if e := <-done; e == nil {
		t.Fatal("returned revoked data")
	}
	if len(p.cache) != 0 {
		t.Fatal("cached revoked data")
	}
}
func TestRedirectNeverForwardsKey(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	old := http.DefaultTransport
	http.DefaultTransport = source.Client().Transport
	defer func() { http.DefaultTransport = old }()
	u, _ := parseBase(source.URL)
	e := fetch(context.Background(), config{RequestTimeoutSeconds: 2}, u, "/issues/1.json", "secret", &map[string]any{})
	if e == nil || leaked.Load() {
		t.Fatal("redirect followed")
	}
}
func TestServeHTTPRequiresSession(t *testing.T) {
	p := &Plugin{}
	r := httptest.NewRequest("GET", "/api/v1/connection", nil)
	r.Header.Set("Mattermost-User-ID", "spoof")
	w := httptest.NewRecorder()
	p.ServeHTTP(&plugin.Context{}, w, r)
	if w.Code != 401 {
		t.Fatalf("spoof accepted: %d", w.Code)
	}
}
