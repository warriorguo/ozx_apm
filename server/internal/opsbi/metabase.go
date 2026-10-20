package opsbi

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Metabase provisioning (cmd/metabase-provision): creates or updates, through
// Metabase's REST API (v0.47 or later), the ClickHouse connection, a
// collection, every preset question (Cards) and the dashboards. Idempotent:
// everything is found by name and only changed when it differs, so running
// it again after an upgrade of this server updates the SQL and changes
// nothing else. Questions and dashboards it did not create are never
// touched; the dashboards it owns are laid out by it (edits made in the UI
// to those dashboards are replaced on the next run).
//
// Secrets (the Metabase password or API key, the ClickHouse password) are
// only sent in request bodies and headers, never written to the log.

// MetabaseConfig is what Provision needs.
type MetabaseConfig struct {
	URL      string
	User     string
	Password string
	APIKey   string // instead of User/Password (Metabase 0.49+)

	DatabaseName   string // the Metabase name of the ClickHouse connection
	CollectionName string

	// The ClickHouse connection (Metabase's ClickHouse driver, HTTP port).
	CHHost     string
	CHPort     int
	CHDatabase string
	CHUser     string
	CHPassword string
	CHSSL      bool
	// UpdateConnection: also send the connection details (with the
	// password) to an existing connection; otherwise an existing one is
	// left as it is (its password cannot be read back to compare).
	UpdateConnection bool
}

// Defaults of the names.
const (
	DefaultDatabaseName   = "OZX Ops (ClickHouse)"
	DefaultCollectionName = "OZX Ops"
)

// Provisioner talks to one Metabase.
type Provisioner struct {
	cfg    MetabaseConfig
	client *http.Client
	log    func(string)
	auth   http.Header
	// Requests counts the API calls by method (tests: a second run makes
	// no POST or PUT).
	Requests map[string]int
}

func NewProvisioner(cfg MetabaseConfig, client *http.Client, log func(string)) *Provisioner {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if log == nil {
		log = func(string) {}
	}
	if cfg.DatabaseName == "" {
		cfg.DatabaseName = DefaultDatabaseName
	}
	if cfg.CollectionName == "" {
		cfg.CollectionName = DefaultCollectionName
	}
	if cfg.CHPort == 0 {
		cfg.CHPort = 8123
	}
	return &Provisioner{cfg: cfg, client: client, log: log, Requests: map[string]int{}}
}

// apiError is a non-2xx answer; its body is shortened and never includes
// what was sent.
type apiError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (p *Provisioner) call(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.cfg.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range p.auth {
		req.Header[k] = v
	}
	p.Requests[method]++
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, p.redact(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return &apiError{method, path, resp.StatusCode, p.redactString(msg)}
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s %s: unexpected answer: %v", method, path, err)
		}
	}
	return nil
}

func (p *Provisioner) redactString(s string) string {
	for _, secret := range []string{p.cfg.Password, p.cfg.APIKey, p.cfg.CHPassword} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return s
}

func (p *Provisioner) redact(err error) error {
	if s := p.redactString(err.Error()); s != err.Error() {
		return errors.New(s)
	}
	return err
}

// Provision runs every step.
func (p *Provisioner) Provision(ctx context.Context) error {
	if err := p.login(ctx); err != nil {
		return err
	}
	dbID, err := p.database(ctx)
	if err != nil {
		return err
	}
	collID, err := p.collection(ctx)
	if err != nil {
		return err
	}
	existing, err := p.collectionItems(ctx, collID)
	if err != nil {
		return err
	}
	cardIDs := map[string]int{}
	for _, c := range Cards {
		id, err := p.card(ctx, c, dbID, collID, existing["card"][c.Name()])
		if err != nil {
			return fmt.Errorf("question %q: %w", c.Name(), err)
		}
		cardIDs[c.Key] = id
	}
	for _, d := range Dashboards {
		if err := p.dashboard(ctx, d, collID, cardIDs, existing["dashboard"][d.Name()]); err != nil {
			return fmt.Errorf("dashboard %q: %w", d.Name(), err)
		}
	}
	return nil
}

func (p *Provisioner) login(ctx context.Context) error {
	p.auth = http.Header{}
	if p.cfg.APIKey != "" {
		p.auth.Set("X-Api-Key", p.cfg.APIKey)
		return nil
	}
	if p.cfg.User == "" || p.cfg.Password == "" {
		return errors.New("Metabase credentials missing: set MB_USER and MB_PASSWORD (or MB_API_KEY)")
	}
	var s struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, http.MethodPost, "/api/session", map[string]string{"username": p.cfg.User, "password": p.cfg.Password}, &s); err != nil {
		return fmt.Errorf("Metabase login failed: %w", err)
	}
	if s.ID == "" {
		return errors.New("Metabase login: no session id in the answer")
	}
	p.auth.Set("X-Metabase-Session", s.ID)
	return nil
}

// list decodes a Metabase list, which is a bare array or {"data": [...]}
// depending on the version and endpoint.
func list(raw json.RawMessage) ([]map[string]interface{}, error) {
	raw = bytes.TrimSpace(raw)
	var items []map[string]interface{}
	if len(raw) > 0 && raw[0] == '[' {
		err := json.Unmarshal(raw, &items)
		return items, err
	}
	var wrapped struct {
		Data []map[string]interface{} `json:"data"`
	}
	err := json.Unmarshal(raw, &wrapped)
	return wrapped.Data, err
}

func intOf(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func (p *Provisioner) details() map[string]interface{} {
	return map[string]interface{}{
		"host":     p.cfg.CHHost,
		"port":     p.cfg.CHPort,
		"dbname":   p.cfg.CHDatabase,
		"user":     p.cfg.CHUser,
		"password": p.cfg.CHPassword,
		"ssl":      p.cfg.CHSSL,
	}
}

func (p *Provisioner) database(ctx context.Context) (int, error) {
	var raw json.RawMessage
	if err := p.call(ctx, http.MethodGet, "/api/database", nil, &raw); err != nil {
		return 0, err
	}
	items, err := list(raw)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		if it["name"] == p.cfg.DatabaseName {
			id := intOf(it["id"])
			if engine, _ := it["engine"].(string); engine != "" && engine != "clickhouse" {
				return 0, fmt.Errorf("Metabase database %q exists with engine %q, not clickhouse", p.cfg.DatabaseName, engine)
			}
			if p.cfg.UpdateConnection {
				if err := p.checkConnection(); err != nil {
					return 0, err
				}
				if err := p.call(ctx, http.MethodPut, "/api/database/"+strconv.Itoa(id), map[string]interface{}{"details": p.details()}, nil); err != nil {
					return 0, err
				}
				p.log(fmt.Sprintf("connection %q (id %d): details updated", p.cfg.DatabaseName, id))
			} else {
				p.log(fmt.Sprintf("connection %q (id %d): exists, left as it is", p.cfg.DatabaseName, id))
			}
			return id, nil
		}
	}
	if err := p.checkConnection(); err != nil {
		return 0, err
	}
	var created map[string]interface{}
	body := map[string]interface{}{"name": p.cfg.DatabaseName, "engine": "clickhouse", "details": p.details(), "is_full_sync": true}
	if err := p.call(ctx, http.MethodPost, "/api/database", body, &created); err != nil {
		return 0, fmt.Errorf("create the ClickHouse connection (is the ClickHouse driver installed in Metabase?): %w", err)
	}
	id := intOf(created["id"])
	if id == 0 {
		return 0, errors.New("create the ClickHouse connection: no id in the answer")
	}
	p.log(fmt.Sprintf("connection %q: created (id %d, %s:%d/%s as %s)", p.cfg.DatabaseName, id, p.cfg.CHHost, p.cfg.CHPort, p.cfg.CHDatabase, p.cfg.CHUser))
	return id, nil
}

func (p *Provisioner) checkConnection() error {
	if p.cfg.CHHost == "" || p.cfg.CHDatabase == "" || p.cfg.CHUser == "" {
		return errors.New("ClickHouse connection incomplete: set CH_HOST, CH_DATABASE, CH_USER (and CH_PASSWORD)")
	}
	return nil
}

func (p *Provisioner) collection(ctx context.Context) (int, error) {
	var raw json.RawMessage
	if err := p.call(ctx, http.MethodGet, "/api/collection", nil, &raw); err != nil {
		return 0, err
	}
	items, err := list(raw)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		if it["name"] == p.cfg.CollectionName && it["archived"] != true && (it["location"] == nil || it["location"] == "/") {
			id := intOf(it["id"])
			p.log(fmt.Sprintf("collection %q (id %d): exists", p.cfg.CollectionName, id))
			return id, nil
		}
	}
	var created map[string]interface{}
	body := map[string]interface{}{"name": p.cfg.CollectionName, "color": "#509EE3", "parent_id": nil,
		"description": "Operations data of OZX (APM-12): presets provisioned by cmd/metabase-provision; edits are replaced on the next run."}
	if err := p.call(ctx, http.MethodPost, "/api/collection", body, &created); err != nil {
		return 0, err
	}
	id := intOf(created["id"])
	p.log(fmt.Sprintf("collection %q: created (id %d)", p.cfg.CollectionName, id))
	return id, nil
}

// collectionItems returns model ("card", "dashboard") -> name -> id.
func (p *Provisioner) collectionItems(ctx context.Context, collID int) (map[string]map[string]int, error) {
	var raw json.RawMessage
	if err := p.call(ctx, http.MethodGet, "/api/collection/"+strconv.Itoa(collID)+"/items?models=card&models=dashboard", nil, &raw); err != nil {
		return nil, err
	}
	items, err := list(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{"card": {}, "dashboard": {}}
	for _, it := range items {
		model, _ := it["model"].(string)
		name, _ := it["name"].(string)
		if out[model] != nil && name != "" {
			out[model][name] = intOf(it["id"])
		}
	}
	return out, nil
}

// stableID derives a stable id from the parts (template tags, parameters):
// the same on every run, so an unchanged question compares equal.
func stableID(n int, parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	s := hex.EncodeToString(h[:])
	if n == 36 {
		return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
	}
	return s[:n]
}

func templateTags(c Card) map[string]interface{} {
	tags := map[string]interface{}{}
	for _, v := range c.Vars {
		tags[v.Name] = map[string]interface{}{
			"id":           stableID(36, "tag", c.Key, v.Name),
			"name":         v.Name,
			"display-name": v.Label,
			"type":         string(v.Type),
			"default":      v.Default,
			"required":     true,
		}
	}
	return tags
}

func visualization(c Card) map[string]interface{} {
	vs := map[string]interface{}{}
	if len(c.Dimensions) > 0 {
		vs["graph.dimensions"] = c.Dimensions
	}
	if len(c.Metrics) > 0 {
		vs["graph.metrics"] = c.Metrics
	}
	return vs
}

// cardBody is what a question is created and updated with.
func cardBody(c Card, dbID, collID int) map[string]interface{} {
	return map[string]interface{}{
		"name":                   c.Name(),
		"description":            c.Description,
		"display":                c.Display,
		"collection_id":          collID,
		"visualization_settings": visualization(c),
		"dataset_query": map[string]interface{}{
			"type":     "native",
			"database": dbID,
			"native": map[string]interface{}{
				"query":         c.SQL,
				"template-tags": templateTags(c),
			},
		},
	}
}

// normalize round-trips v through JSON (numbers as float64), so a body and
// what Metabase answers compare equal when they mean the same.
func normalize(v interface{}) interface{} {
	b, _ := json.Marshal(v)
	var out interface{}
	_ = json.Unmarshal(b, &out)
	return out
}

// sameCard compares the fields this tool sets.
func sameCard(want map[string]interface{}, got map[string]interface{}) bool {
	w, g := normalize(want).(map[string]interface{}), normalize(got).(map[string]interface{})
	for _, k := range []string{"name", "description", "display", "collection_id", "visualization_settings"} {
		if !reflect.DeepEqual(w[k], g[k]) {
			return false
		}
	}
	wq, _ := w["dataset_query"].(map[string]interface{})
	gq, _ := g["dataset_query"].(map[string]interface{})
	if wq == nil || gq == nil || !reflect.DeepEqual(wq["type"], gq["type"]) || !reflect.DeepEqual(wq["database"], gq["database"]) {
		return false
	}
	wn, _ := wq["native"].(map[string]interface{})
	gn, _ := gq["native"].(map[string]interface{})
	if wn == nil || gn == nil || wn["query"] != gn["query"] {
		return false
	}
	wt, _ := wn["template-tags"].(map[string]interface{})
	gt, _ := gn["template-tags"].(map[string]interface{})
	if len(wt) != len(gt) {
		return false
	}
	for name, w := range wt {
		g, ok := gt[name].(map[string]interface{})
		if !ok {
			return false
		}
		for k, v := range w.(map[string]interface{}) {
			// Loosely: Metabase may answer a number tag's default "30" as 30.
			if fmt.Sprint(v) != fmt.Sprint(g[k]) {
				return false
			}
		}
	}
	return true
}

func (p *Provisioner) card(ctx context.Context, c Card, dbID, collID, id int) (int, error) {
	body := cardBody(c, dbID, collID)
	if id == 0 {
		var created map[string]interface{}
		if err := p.call(ctx, http.MethodPost, "/api/card", body, &created); err != nil {
			return 0, err
		}
		id = intOf(created["id"])
		p.log(fmt.Sprintf("question %q: created (id %d)", c.Name(), id))
		return id, nil
	}
	var current map[string]interface{}
	if err := p.call(ctx, http.MethodGet, "/api/card/"+strconv.Itoa(id), nil, &current); err != nil {
		return 0, err
	}
	if sameCard(body, current) {
		p.log(fmt.Sprintf("question %q (id %d): unchanged", c.Name(), id))
		return id, nil
	}
	if err := p.call(ctx, http.MethodPut, "/api/card/"+strconv.Itoa(id), body, nil); err != nil {
		return 0, err
	}
	p.log(fmt.Sprintf("question %q (id %d): updated", c.Name(), id))
	return id, nil
}

// Dashboard layout: Metabase's grid is 24 columns wide (0.46+). Charts take
// half a row, tables a full row.
const gridWidth = 24

func dashParameters(d Dashboard) []map[string]interface{} {
	var out []map[string]interface{}
	for _, v := range d.VarsOf() {
		typ := "string/="
		var def interface{} = []string{v.Default}
		if v.Type == VarNumber {
			typ = "number/="
			f, _ := strconv.ParseFloat(v.Default, 64)
			def = []float64{f}
		}
		p := map[string]interface{}{
			"id":   stableID(8, "param", d.Key, v.Name),
			"name": v.Label,
			"slug": v.Name,
			"type": typ,
		}
		if v.Default != "" {
			p["default"] = def
		}
		out = append(out, p)
	}
	return out
}

type dashcard struct {
	CardID   int
	Row, Col int
	SizeX    int
	SizeY    int
	Mappings []map[string]interface{}
}

func dashLayout(d Dashboard, cardIDs map[string]int) []dashcard {
	params := map[string]string{}
	for _, v := range d.VarsOf() {
		params[v.Name] = stableID(8, "param", d.Key, v.Name)
	}
	var out []dashcard
	row, col, rowH := 0, 0, 0
	for _, key := range d.Cards {
		c, _ := CardByKey(key)
		w, h := gridWidth/2, 6
		if c.Display == "table" {
			w, h = gridWidth, 8
		}
		if col+w > gridWidth {
			row += rowH
			col, rowH = 0, 0
		}
		if h > rowH {
			rowH = h
		}
		var maps []map[string]interface{}
		names := make([]string, 0, len(c.Vars))
		for _, v := range c.Vars {
			names = append(names, v.Name)
		}
		sort.Strings(names)
		for _, n := range names {
			maps = append(maps, map[string]interface{}{
				"parameter_id": params[n],
				"card_id":      cardIDs[key],
				"target":       []interface{}{"variable", []interface{}{"template-tag", n}},
			})
		}
		out = append(out, dashcard{CardID: cardIDs[key], Row: row, Col: col, SizeX: w, SizeY: h, Mappings: maps})
		col += w
		if col >= gridWidth {
			row += rowH
			col, rowH = 0, 0
		}
	}
	return out
}

func (p *Provisioner) dashboard(ctx context.Context, d Dashboard, collID int, cardIDs map[string]int, id int) error {
	params := dashParameters(d)
	if id == 0 {
		var created map[string]interface{}
		body := map[string]interface{}{"name": d.Name(), "description": d.Description, "collection_id": collID, "parameters": params}
		if err := p.call(ctx, http.MethodPost, "/api/dashboard", body, &created); err != nil {
			return err
		}
		id = intOf(created["id"])
		p.log(fmt.Sprintf("dashboard %q: created (id %d)", d.Name(), id))
	}
	var current map[string]interface{}
	if err := p.call(ctx, http.MethodGet, "/api/dashboard/"+strconv.Itoa(id), nil, &current); err != nil {
		return err
	}
	existing, _ := current["dashcards"].([]interface{})
	if existing == nil {
		existing, _ = current["ordered_cards"].([]interface{}) // before 0.47
	}
	// Reuse the dashcard ids of the cards already on it.
	byCard := map[int]int{}
	for _, raw := range existing {
		dc, _ := raw.(map[string]interface{})
		if cid := intOf(dc["card_id"]); cid != 0 {
			byCard[cid] = intOf(dc["id"])
		}
	}
	layout := dashLayout(d, cardIDs)
	var dashcards []map[string]interface{}
	for i, l := range layout {
		dcID, ok := byCard[l.CardID]
		if !ok {
			dcID = -(i + 1)
		}
		dashcards = append(dashcards, map[string]interface{}{
			"id": dcID, "card_id": l.CardID, "row": l.Row, "col": l.Col, "size_x": l.SizeX, "size_y": l.SizeY,
			"parameter_mappings": l.Mappings, "visualization_settings": map[string]interface{}{},
		})
	}
	want := map[string]interface{}{"name": d.Name(), "description": d.Description, "collection_id": collID, "parameters": params, "dashcards": dashcards}
	if sameDashboard(want, current) {
		p.log(fmt.Sprintf("dashboard %q (id %d): unchanged", d.Name(), id))
		return nil
	}
	if err := p.call(ctx, http.MethodPut, "/api/dashboard/"+strconv.Itoa(id), want, nil); err != nil {
		return err
	}
	p.log(fmt.Sprintf("dashboard %q (id %d): laid out (%d questions)", d.Name(), id, len(dashcards)))
	return nil
}

// sameDashboard compares the fields this tool sets (dashcards by card,
// position, size and parameter mappings).
func sameDashboard(want, got map[string]interface{}) bool {
	w, g := normalize(want).(map[string]interface{}), normalize(got).(map[string]interface{})
	for _, k := range []string{"name", "description", "collection_id"} {
		if !reflect.DeepEqual(w[k], g[k]) {
			return false
		}
	}
	if !sameParams(w["parameters"], g["parameters"]) {
		return false
	}
	gc, _ := g["dashcards"].([]interface{})
	if gc == nil {
		gc, _ = g["ordered_cards"].([]interface{})
	}
	wc, _ := w["dashcards"].([]interface{})
	if len(wc) != len(gc) {
		return false
	}
	key := func(raw interface{}) string {
		m, _ := raw.(map[string]interface{})
		b, _ := json.Marshal([]interface{}{m["card_id"], m["row"], m["col"], m["size_x"], m["size_y"], mappingKey(m["parameter_mappings"])})
		return string(b)
	}
	var ws, gs []string
	for i := range wc {
		ws = append(ws, key(wc[i]))
		gs = append(gs, key(gc[i]))
	}
	sort.Strings(ws)
	sort.Strings(gs)
	return reflect.DeepEqual(ws, gs)
}

func mappingKey(v interface{}) string {
	list, _ := v.([]interface{})
	var keys []string
	for _, raw := range list {
		m, _ := raw.(map[string]interface{})
		b, _ := json.Marshal([]interface{}{m["parameter_id"], m["card_id"], m["target"]})
		keys = append(keys, string(b))
	}
	sort.Strings(keys)
	return strings.Join(keys, "|")
}

func sameParams(w, g interface{}) bool {
	wl, _ := w.([]interface{})
	gl, _ := g.([]interface{})
	if len(wl) != len(gl) {
		return false
	}
	pick := func(raw interface{}) string {
		m, _ := raw.(map[string]interface{})
		// Loosely (fmt): a default [30] may come back as ["30"].
		return fmt.Sprint(m["id"], "|", m["name"], "|", m["slug"], "|", m["type"], "|", m["default"])
	}
	var ws, gs []string
	for i := range wl {
		ws = append(ws, pick(wl[i]))
		gs = append(gs, pick(gl[i]))
	}
	sort.Strings(ws)
	sort.Strings(gs)
	return reflect.DeepEqual(ws, gs)
}
