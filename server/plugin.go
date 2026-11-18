package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

const pluginID = "space.altlinux.redmine"
const serverTTL = 90 * time.Second
const clientTTL = 300 * time.Second

var number = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

type config struct {
	RedmineURL            string
	EncryptionKey         string
	RequestTimeoutSeconds int
}
type connection struct {
	Revision    string `json:"revision"`
	Ciphertext  string `json:"ciphertext"`
	AccountID   int    `json:"account_id"`
	DisplayName string `json:"display_name"`
	Instance    string `json:"instance"`
}
type named struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type redmineIssue struct {
	ID          int    `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Status      named  `json:"status"`
	Priority    named  `json:"priority"`
	Assigned    named  `json:"assigned_to"`
	Project     named  `json:"project"`
	Tracker     named  `json:"tracker"`
	Updated     string `json:"updated_on"`
}
type card struct {
	ID              int    `json:"id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	Assignee        string `json:"assignee"`
	Project         string `json:"project"`
	Tracker         string `json:"tracker"`
	Updated         string `json:"updated"`
	URL             string `json:"url"`
	SourceFetchedAt int64  `json:"source_fetched_at"`
	ExpiresAt       int64  `json:"expires_at"`
	Revision        string `json:"revision"`
}
type cached struct {
	Card  card
	Until time.Time
}
type flight struct {
	done chan struct{}
	card card
	err  error
}
type Plugin struct {
	plugin.MattermostPlugin
	mu      sync.Mutex
	cache   map[string]cached
	flights map[string]*flight
	slots   chan struct{}
	stop    chan struct{}
}
type apiError struct {
	status int
	code   string
}

func (e *apiError) Error() string { return e.code }

func (p *Plugin) OnActivate() error {
	p.mu.Lock()
	p.cache = map[string]cached{}
	p.flights = map[string]*flight{}
	p.slots = make(chan struct{}, 8)
	p.stop = make(chan struct{})
	stop := p.stop
	p.mu.Unlock()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				p.mu.Lock()
				for k, v := range p.cache {
					if !v.Until.After(now) {
						delete(p.cache, k)
					}
				}
				p.mu.Unlock()
			}
		}
	}()
	return nil
}
func (p *Plugin) OnDeactivate() error {
	p.mu.Lock()
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
	p.mu.Unlock()
	return nil
}
func (p *Plugin) OnConfigurationChange() error {
	p.API.PublishWebSocketEvent("configuration_changed", map[string]interface{}{}, nil)
	return nil
}

func (p *Plugin) configuration() (config, *url.URL, error) {
	var c config
	if err := p.API.LoadPluginConfiguration(&c); err != nil {
		return c, nil, err
	}
	u, err := parseBase(c.RedmineURL)
	if err != nil {
		return c, nil, err
	}
	if c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 60 {
		c.RequestTimeoutSeconds = 15
	}
	return c, u, nil
}
func parseBase(raw string) (*url.URL, error) {
	u, e := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("configure an HTTPS Redmine URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}
func randomID() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func storageKey(user string) string {
	d := sha256.Sum256([]byte(user))
	return "connection_" + hex.EncodeToString(d[:])
}
func (p *Plugin) load(user string) (connection, []byte, error) {
	raw, e := p.API.KVGet(storageKey(user))
	if e != nil {
		return connection{}, nil, e
	}
	var c connection
	if len(raw) > 0 {
		if e := json.Unmarshal(raw, &c); e != nil {
			return c, nil, e
		}
	}
	return c, raw, nil
}
func encryption(c config) (cipher.AEAD, error) {
	key := os.Getenv("MM_REDMINE_ENCRYPTION_KEY")
	if file := os.Getenv("MM_REDMINE_ENCRYPTION_KEY_FILE"); key == "" && file != "" {
		b, e := os.ReadFile(file)
		if e != nil {
			return nil, errors.New("encryption key file unavailable")
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		key = c.EncryptionKey
	}
	b, e := base64.StdEncoding.DecodeString(key)
	if e != nil || len(b) != 32 {
		return nil, errors.New("administrator must configure a base64 32-byte encryption key")
	}
	block, e := aes.NewCipher(b)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func seal(c config, user, instance, key string) (string, error) {
	a, e := encryption(c)
	if e != nil {
		return "", e
	}
	n := make([]byte, a.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return "", e
	}
	out := a.Seal(n, n, []byte(key), []byte(user+"\x00"+instance))
	return base64.StdEncoding.EncodeToString(out), nil
}
func unseal(c config, user string, conn connection) (string, error) {
	a, e := encryption(c)
	if e != nil {
		return "", e
	}
	b, e := base64.StdEncoding.DecodeString(conn.Ciphertext)
	if e != nil || len(b) < a.NonceSize() {
		return "", errors.New("invalid encrypted key")
	}
	out, e := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(user+"\x00"+conn.Instance))
	return string(out), e
}
func (p *Plugin) save(user string, c connection, old []byte) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	ok, ae := p.API.KVCompareAndSet(storageKey(user), old, b)
	if ae != nil {
		return ae
	}
	if !ok {
		return &apiError{409, "connection_changed"}
	}
	p.API.PublishWebSocketEvent("connection_changed", map[string]interface{}{"revision": c.Revision}, &model.WebsocketBroadcast{UserId: user})
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	var a *apiError
	if errors.As(e, &a) {
		writeJSON(w, a.status, map[string]string{"error": a.code})
		return
	}
	writeJSON(w, 502, map[string]string{"error": "upstream_unavailable"})
}
func validOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, e := url.Parse(o)
	return e == nil && strings.EqualFold(u.Host, r.Host)
}
func (p *Plugin) ServeHTTP(ctx *plugin.Context, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := r.Header.Get("Mattermost-User-ID")
	if user == "" || ctx == nil || ctx.SessionId == "" {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	session, se := p.API.GetSession(ctx.SessionId)
	if se != nil || session == nil || session.UserId != user || session.IsExpired() {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	if r.Method != "GET" && !validOrigin(r) {
		writeJSON(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	c, u, e := p.configuration()
	if e != nil {
		writeJSON(w, 503, map[string]string{"error": "server_not_configured"})
		return
	}
	conn, raw, e := p.load(user)
	if e != nil {
		fail(w, e)
		return
	}
	instance := u.String()
	switch {
	case r.URL.Path == "/api/v1/connection" && r.Method == "GET":
		writeJSON(w, 200, map[string]interface{}{"connected": conn.Ciphertext != "" && conn.Instance == instance, "revision": conn.Revision, "display_name": conn.DisplayName, "server_url": instance, "client_ttl_seconds": 300})
		return
	case r.URL.Path == "/api/v1/connection" && r.Method == "POST":
		var in struct {
			APIKey string `json:"api_key"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil || len(in.APIKey) < 1 || len(in.APIKey) > 4096 || strings.ContainsAny(in.APIKey, "\r\n") {
			writeJSON(w, 400, map[string]string{"error": "invalid_api_key"})
			return
		}
		var me struct {
			User struct {
				ID    int    `json:"id"`
				First string `json:"firstname"`
				Last  string `json:"lastname"`
			} `json:"user"`
		}
		if e := fetch(r.Context(), c, u, "/users/current.json", in.APIKey, &me); e != nil {
			fail(w, e)
			return
		}
		if me.User.ID == 0 {
			fail(w, &apiError{401, "invalid_api_key"})
			return
		}
		encrypted, e := seal(c, user, instance, in.APIKey)
		if e != nil {
			writeJSON(w, 503, map[string]string{"error": "encryption_not_configured"})
			return
		}
		newConn := connection{randomID(), encrypted, me.User.ID, strings.TrimSpace(me.User.First + " " + me.User.Last), instance}
		if e = p.save(user, newConn, raw); e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]string{"revision": newConn.Revision})
		return
	case r.URL.Path == "/api/v1/connection" && r.Method == "DELETE":
		if e = p.save(user, connection{Revision: randomID(), Instance: instance}, raw); e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	case strings.HasPrefix(r.URL.Path, "/api/v1/issues/") && r.Method == "GET":
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/issues/")
		if !number.MatchString(id) {
			writeJSON(w, 400, map[string]string{"error": "invalid_issue_id"})
			return
		}
		if conn.Ciphertext == "" || conn.Instance != instance {
			writeJSON(w, 401, map[string]string{"error": "connect_required"})
			return
		}
		result, e := p.getCard(user, id, conn, c, u, r.URL.Query().Get("refresh") == "1")
		if e != nil {
			fail(w, e)
			return
		}
		latest, _, e := p.load(user)
		if e != nil || latest.Revision != conn.Revision || latest.Ciphertext == "" {
			writeJSON(w, 409, map[string]string{"error": "connection_changed"})
			return
		}
		writeJSON(w, 200, struct {
			card
			ServerNow int64 `json:"server_now"`
		}{result, time.Now().UnixMilli()})
		return
	default:
		writeJSON(w, 404, map[string]string{"error": "not_found"})
	}
}
func fetch(ctx context.Context, c config, u *url.URL, path, key string, out interface{}) error {
	q := *u
	q.Path = strings.TrimRight(u.Path, "/") + path
	req, e := http.NewRequestWithContext(ctx, "GET", q.String(), nil)
	if e != nil {
		return e
	}
	req.Header.Set("X-Redmine-API-Key", key)
	req.Header.Set("Accept", "application/json")
	client := http.Client{Timeout: time.Duration(c.RequestTimeoutSeconds) * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return errors.New("Redmine request failed")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return &apiError{401, "invalid_api_key"}
	}
	if res.StatusCode == 403 || res.StatusCode == 404 {
		return &apiError{404, "unavailable"}
	}
	if res.StatusCode != 200 {
		return errors.New("unexpected Redmine response")
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); e != nil {
		return errors.New("invalid Redmine response")
	}
	return nil
}
func (p *Plugin) getCard(user, id string, conn connection, c config, u *url.URL, force bool) (card, error) {
	key := user + "|" + u.String() + "|" + conn.Revision + "|" + id
	p.mu.Lock()
	now := time.Now()
	for k, v := range p.cache {
		if !v.Until.After(now) {
			delete(p.cache, k)
		}
	}
	if v, ok := p.cache[key]; ok && !force {
		p.mu.Unlock()
		return v.Card, nil
	}
	if f, ok := p.flights[key]; ok {
		p.mu.Unlock()
		<-f.done
		return f.card, f.err
	}
	if len(p.flights) >= 64 {
		p.mu.Unlock()
		return card{}, &apiError{429, "busy"}
	}
	f := &flight{done: make(chan struct{})}
	p.flights[key] = f
	p.mu.Unlock()
	result, err := func() (card, error) {
		p.slots <- struct{}{}
		defer func() { <-p.slots }()
		key, e := unseal(c, user, conn)
		if e != nil {
			return card{}, errors.New("key unavailable")
		}
		var response struct {
			Issue redmineIssue `json:"issue"`
		}
		if e = fetch(context.Background(), c, u, "/issues/"+id+".json", key, &response); e != nil {
			return card{}, e
		}
		if strconv.Itoa(response.Issue.ID) != id {
			return card{}, errors.New("invalid issue")
		}
		latest, _, e := p.load(user)
		if e != nil || latest.Revision != conn.Revision || latest.Ciphertext == "" {
			return card{}, &apiError{409, "connection_changed"}
		}
		i := response.Issue
		now := time.Now()
		return card{i.ID, i.Subject, truncate(i.Description, 600), i.Status.Name, i.Priority.Name, i.Assigned.Name, i.Project.Name, i.Tracker.Name, i.Updated, u.String() + "/issues/" + id, now.UnixMilli(), now.Add(clientTTL).UnixMilli(), conn.Revision}, nil
	}()
	p.mu.Lock()
	if err == nil {
		if len(p.cache) >= 1024 {
			for k := range p.cache {
				delete(p.cache, k)
				break
			}
		}
		p.cache[key] = cached{result, time.UnixMilli(result.SourceFetchedAt).Add(serverTTL)}
	} else {
		delete(p.cache, key)
	}
	f.card = result
	f.err = err
	delete(p.flights, key)
	close(f.done)
	p.mu.Unlock()
	return result, err
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
