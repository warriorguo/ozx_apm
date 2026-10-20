package opsbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeMetabase implements the part of Metabase's REST API (0.47+) the
// provisioner uses, in memory, with Metabase's answer shapes.
type fakeMetabase struct {
	mu          sync.Mutex
	user, pass  string
	apiKey      string
	session     string
	nextID      int
	databases   []map[string]interface{}
	collections []map[string]interface{}
	cards       map[int]map[string]interface{}
	dashboards  map[int]map[string]interface{}
	writes      []string // "METHOD path" of every POST/PUT
	bodies      []string // every request body
}

func newFakeMetabase() *fakeMetabase {
	return &fakeMetabase{user: "admin@example.com", pass: "mb-pass-123", apiKey: "mb_key_456", nextID: 100,
		cards: map[int]map[string]interface{}{}, dashboards: map[int]map[string]interface{}{}}
}

func (f *fakeMetabase) id() int { f.nextID++; return f.nextID }

func (f *fakeMetabase) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]interface{}
	if r.Body != nil {
		var raw json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&raw)
		f.bodies = append(f.bodies, string(raw))
		_ = json.Unmarshal(raw, &body)
	}
	reply := func(v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	path := r.URL.Path
	if r.Method == http.MethodPost && path == "/api/session" {
		if body["username"] != f.user || body["password"] != f.pass {
			http.Error(w, `{"errors":{"password":"did not match stored password"}}`, http.StatusUnauthorized)
			return
		}
		f.session = "sess-1"
		reply(map[string]string{"id": f.session})
		return
	}
	if !(f.session != "" && r.Header.Get("X-Metabase-Session") == f.session) && r.Header.Get("X-Api-Key") != f.apiKey {
		http.Error(w, "Unauthenticated", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		f.writes = append(f.writes, r.Method+" "+path)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	num := func(i int) int { n, _ := strconv.Atoi(parts[i]); return n }
	switch {
	case r.Method == http.MethodGet && path == "/api/database":
		reply(map[string]interface{}{"data": f.databases, "total": len(f.databases)})
	case r.Method == http.MethodPost && path == "/api/database":
		body["id"] = f.id()
		f.databases = append(f.databases, body)
		reply(body)
	case r.Method == http.MethodPut && len(parts) == 3 && parts[1] == "database":
		for _, d := range f.databases {
			if intOf(d["id"]) == num(2) {
				d["details"] = body["details"]
			}
		}
		reply(map[string]interface{}{})
	case r.Method == http.MethodGet && path == "/api/collection":
		reply(f.collections)
	case r.Method == http.MethodPost && path == "/api/collection":
		body["id"] = f.id()
		body["location"] = "/"
		f.collections = append(f.collections, body)
		reply(body)
	case r.Method == http.MethodGet && len(parts) == 4 && parts[1] == "collection" && parts[3] == "items":
		var items []map[string]interface{}
		for id, c := range f.cards {
			if intOf(c["collection_id"]) == num(2) {
				items = append(items, map[string]interface{}{"id": id, "name": c["name"], "model": "card"})
			}
		}
		for id, d := range f.dashboards {
			if intOf(d["collection_id"]) == num(2) {
				items = append(items, map[string]interface{}{"id": id, "name": d["name"], "model": "dashboard"})
			}
		}
		reply(map[string]interface{}{"data": items, "total": len(items)})
	case r.Method == http.MethodPost && path == "/api/card":
		id := f.id()
		body["id"] = id
		body["archived"] = false
		f.cards[id] = body
		reply(body)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "card":
		c := f.cards[num(2)]
		out := map[string]interface{}{"creator_id": 1, "result_metadata": []interface{}{}}
		for k, v := range c {
			out[k] = v
		}
		reply(out)
	case r.Method == http.MethodPut && len(parts) == 3 && parts[1] == "card":
		body["id"] = num(2)
		f.cards[num(2)] = body
		reply(body)
	case r.Method == http.MethodPost && path == "/api/dashboard":
		id := f.id()
		body["id"] = id
		body["dashcards"] = []interface{}{}
		f.dashboards[id] = body
		reply(body)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "dashboard":
		d := f.dashboards[num(2)]
		out := map[string]interface{}{"can_write": true}
		for k, v := range d {
			out[k] = v
		}
		reply(out)
	case r.Method == http.MethodPut && len(parts) == 3 && parts[1] == "dashboard":
		d := f.dashboards[num(2)]
		for k, v := range body {
			d[k] = v
		}
		if dcs, ok := body["dashcards"].([]interface{}); ok {
			for _, raw := range dcs {
				dc := raw.(map[string]interface{})
				if intOf(dc["id"]) < 0 {
					dc["id"] = f.id()
				}
				dc["dashboard_id"] = num(2)
			}
		}
		reply(d)
	default:
		http.Error(w, "not found: "+r.Method+" "+path, http.StatusNotFound)
	}
}

type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

func testConfig(f *fakeMetabase) MetabaseConfig {
	return MetabaseConfig{URL: "http://metabase.test/", User: f.user, Password: f.pass,
		CHHost: "clickhouse", CHPort: 8123, CHDatabase: "ozx_apm", CHUser: "ozx_ops_ro", CHPassword: "ch-secret-789"}
}

func run(t *testing.T, f *fakeMetabase, cfg MetabaseConfig) (*Provisioner, []string, error) {
	t.Helper()
	var logs []string
	p := NewProvisioner(cfg, &http.Client{Transport: handlerTransport{f}}, func(s string) { logs = append(logs, s) })
	err := p.Provision(context.Background())
	return p, logs, err
}

func TestProvision_CreatesEverythingThenChangesNothing(t *testing.T) {
	f := newFakeMetabase()
	cfg := testConfig(f)
	p, logs, err := run(t, f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.databases) != 1 || f.databases[0]["engine"] != "clickhouse" || len(f.collections) != 1 ||
		len(f.cards) != len(Cards) || len(f.dashboards) != len(Dashboards) {
		t.Fatalf("db %d coll %d cards %d dash %d", len(f.databases), len(f.collections), len(f.cards), len(f.dashboards))
	}
	det := f.databases[0]["details"].(map[string]interface{})
	if det["host"] != "clickhouse" || intOf(det["port"]) != 8123 || det["dbname"] != "ozx_apm" || det["user"] != "ozx_ops_ro" || det["password"] != "ch-secret-789" {
		t.Errorf("details %v", det)
	}
	// Every card is native SQL on the connection with its template tags.
	for _, c := range f.cards {
		q := c["dataset_query"].(map[string]interface{})
		n := q["native"].(map[string]interface{})
		if q["type"] != "native" || intOf(q["database"]) != intOf(f.databases[0]["id"]) || n["query"] == "" || len(n["template-tags"].(map[string]interface{})) == 0 {
			t.Errorf("card %v", c["name"])
		}
	}
	// Every dashboard holds its cards with every filter mapped.
	for _, d := range f.dashboards {
		var want Dashboard
		for _, x := range Dashboards {
			if x.Name() == d["name"] {
				want = x
			}
		}
		dcs := d["dashcards"].([]interface{})
		if len(dcs) != len(want.Cards) || len(d["parameters"].([]interface{})) != len(want.VarsOf()) {
			t.Errorf("dashboard %v: %d cards, %d parameters", d["name"], len(dcs), len(d["parameters"].([]interface{})))
		}
		for _, raw := range dcs {
			dc := raw.(map[string]interface{})
			if intOf(dc["id"]) <= 0 || len(dc["parameter_mappings"].([]interface{})) == 0 {
				t.Errorf("dashcard %v", dc)
			}
		}
	}
	for _, l := range logs {
		for _, secret := range []string{f.pass, "ch-secret-789", f.apiKey} {
			if strings.Contains(l, secret) {
				t.Fatalf("secret in the log: %s", l)
			}
		}
	}
	if p.Requests["POST"] != 1+1+1+len(Cards)+len(Dashboards) {
		t.Errorf("requests %v", p.Requests)
	}

	// Second run: nothing created or changed.
	writes := len(f.writes)
	p, logs, err = run(t, f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != writes || p.Requests["POST"] != 1 || p.Requests["PUT"] != 0 {
		t.Fatalf("second run wrote %v (requests %v)", f.writes[writes:], p.Requests)
	}
	if len(f.cards) != len(Cards) || len(f.dashboards) != len(Dashboards) {
		t.Fatal("duplicates")
	}
	for _, l := range logs {
		if strings.Contains(l, "created") || strings.Contains(l, "updated") || strings.Contains(l, "laid out") {
			t.Errorf("second run: %s", l)
		}
	}
}

// A question edited in the UI and a dashboard rearranged are put back; a
// question of someone else in the collection is never touched.
func TestProvision_RestoresChangedPresetsOnly(t *testing.T) {
	f := newFakeMetabase()
	cfg := testConfig(f)
	if _, _, err := run(t, f, cfg); err != nil {
		t.Fatal(err)
	}
	collID := intOf(f.collections[0]["id"])
	f.cards[999] = map[string]interface{}{"id": 999, "name": "My own question", "collection_id": collID}
	var edited, dashID int
	for id, c := range f.cards {
		if c["name"] == Cards[0].Name() {
			edited = id
			c["dataset_query"].(map[string]interface{})["native"].(map[string]interface{})["query"] = "SELECT 1"
		}
	}
	for id, d := range f.dashboards {
		dashID = id
		dcs := d["dashcards"].([]interface{})
		dcs[0].(map[string]interface{})["col"] = 3.0
		break
	}
	writes := len(f.writes)
	if _, _, err := run(t, f, cfg); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.writes[writes:], ",")
	want := "PUT /api/card/" + strconv.Itoa(edited) + ",PUT /api/dashboard/" + strconv.Itoa(dashID)
	if got != want {
		t.Errorf("writes %s, want %s", got, want)
	}
	if f.cards[999]["name"] != "My own question" || len(f.cards) != len(Cards)+1 {
		t.Error("someone else's question was touched")
	}
	q := f.cards[edited]["dataset_query"].(map[string]interface{})["native"].(map[string]interface{})["query"]
	if q != Cards[0].SQL {
		t.Error("the preset SQL was not restored")
	}
}

func TestProvision_ConnectionRules(t *testing.T) {
	// An existing connection is left alone unless asked; with the API key.
	f := newFakeMetabase()
	f.databases = []map[string]interface{}{{"id": 7, "name": DefaultDatabaseName, "engine": "clickhouse", "details": map[string]interface{}{"host": "old"}}}
	cfg := testConfig(f)
	cfg.User, cfg.Password, cfg.APIKey = "", "", f.apiKey
	if _, _, err := run(t, f, cfg); err != nil {
		t.Fatal(err)
	}
	if f.databases[0]["details"].(map[string]interface{})["host"] != "old" || len(f.databases) != 1 {
		t.Error("existing connection changed without UpdateConnection")
	}
	cfg.UpdateConnection = true
	if _, _, err := run(t, f, cfg); err != nil {
		t.Fatal(err)
	}
	if f.databases[0]["details"].(map[string]interface{})["host"] != "clickhouse" {
		t.Error("UpdateConnection did not update")
	}

	// A connection of that name to another engine: refused.
	f = newFakeMetabase()
	f.databases = []map[string]interface{}{{"id": 7, "name": DefaultDatabaseName, "engine": "postgres"}}
	if _, _, err := run(t, f, testConfig(f)); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Errorf("other engine: %v", err)
	}

	// Wrong password: an error without the password in it.
	f = newFakeMetabase()
	cfg = testConfig(f)
	cfg.Password = "wrong-pass-000"
	if _, _, err := run(t, f, cfg); err == nil || strings.Contains(err.Error(), "wrong-pass-000") || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
	// Missing settings.
	if _, _, err := run(t, f, MetabaseConfig{URL: "http://metabase.test"}); err == nil || !strings.Contains(err.Error(), "MB_USER") {
		t.Errorf("no credentials: %v", err)
	}
	cfg = testConfig(f)
	cfg.CHHost = ""
	if _, _, err := run(t, f, cfg); err == nil || !strings.Contains(err.Error(), "CH_HOST") {
		t.Errorf("no connection: %v", err)
	}
}

// Dashboard layout: within the 24-column grid, no overlap.
func TestDashLayout(t *testing.T) {
	ids := map[string]int{}
	for i, c := range Cards {
		ids[c.Key] = i + 1
	}
	for _, d := range Dashboards {
		type cell struct{ r, c int }
		used := map[cell]bool{}
		for _, l := range dashLayout(d, ids) {
			if l.Col+l.SizeX > gridWidth {
				t.Errorf("%s: card %d beyond the grid", d.Key, l.CardID)
			}
			for r := l.Row; r < l.Row+l.SizeY; r++ {
				for c := l.Col; c < l.Col+l.SizeX; c++ {
					if used[cell{r, c}] {
						t.Fatalf("%s: overlap at %d,%d", d.Key, r, c)
					}
					used[cell{r, c}] = true
				}
			}
		}
	}
}
