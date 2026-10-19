// Package sessionexport writes one session's stored events as a session
// directory in the format of OZX's AiTelemetry (APM-10), so that OZX's
// offline tools (scripts/ai_trace.py, the verify-ticket skill) read an online
// session as they read a session recorded in the Editor
// (Documents/Shared/AITelemetry.md in the OZX repository):
//
//	<dir>/session.json         AiTelemetry's session metadata (what APM knows of it) + "apm"
//	<dir>/<channel>.jsonl      the custom events named "<channel>.<ev>" (ApmClient.Event):
//	                           playermovement.jsonl, encounter.jsonl, runstart.jsonl, loot.jsonl, ...
//	<dir>/custom.jsonl         other custom events (no channel in the name)
//	<dir>/apm.jsonl            APM's own events: exceptions, crashes, janks, scene loads,
//	                           startup, runs, metric windows, perf samples, and custom
//	                           events without the client's game clock (counts, old SDKs)
//
// Every line of a channel file (and of custom.jsonl) has AiTelemetry's
// leading fields "t" (the game time the client reported, 4 decimals), "frame",
// "run" and "ev", then the event's properties with their JSON types (numbers
// and booleans as such; a vector sent as key.x/key.y is written as
// {"x":..,"y":..}). Only events that carry the client's game clock are
// written there; the server never derives t or frame. Lines of apm.jsonl have
// "run", "ev" (apm_*), "ts" (the event's wall-clock time, UTC) and t/frame
// when the client sent them.
//
// "run" is AiTelemetry's run number in the session: the n-th distinct value
// of the game context key runId (ApmClient.BeginRun, APM-9) in the session's
// time order, 1-based; 0 before the first run and outside runs. session.json
// lists the run ids ("apm.runs").
//
// Untrusted text (names, properties, messages) is written as JSON strings
// (encoding/json: escaped quotes and control characters, U+2028/2029,
// invalid UTF-8 replaced): every line is one valid JSON object whatever the
// content. File names come from channel names restricted to [a-z0-9_].
package sessionexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FormatVersion is written to session.json ("apm.format").
const FormatVersion = 1

// MaxBytes bounds the uncompressed size of the files: lines are added in
// time order until the next one would exceed it ("bytes" in truncated).
const MaxBytes = 32 << 20

var maxBytes = MaxBytes // tests lower it

// Files that are not channels.
const (
	FileSession = "session.json"
	FileAPM     = "apm.jsonl"
	FileCustom  = "custom.jsonl"
)

// Sources of rows (the event tables).
const (
	SourceCustomEvent  = "custom_event"
	SourceCustomMetric = "custom_metric"
	SourceException    = "exception"
	SourceCrash        = "crash"
	SourceJank         = "jank"
	SourceSceneLoad    = "scene_load"
	SourceStartup      = "startup"
	SourceRun          = "run"
	SourcePerf         = "perf_sample"
)

// Property types (models.PropTypeNumber / PropTypeBool; anything else is a string).
const (
	propNumber uint8 = 1
	propBool   uint8 = 2
)

// Field is one named value of an APM line, in order. Value is a string, a
// float64, an int64, a bool, nil, or a []string.
type Field struct {
	Key   string
	Value any
}

// Row is one stored event of the session.
type Row struct {
	Source string
	Time   time.Time
	Seq    uint64
	GameT  *float64 // the client's game clock; nil when the event has none
	Frame  *int64
	RunID  string // the game context's runId (for runs: run_id)
	// Custom events.
	Name       string
	Count      int64
	Aggregated bool
	CtxMerged  bool
	Props      map[string]string
	PropTypes  map[string]uint8
	// APM events: their fields, in order (after ts, t, frame, run, ev).
	Fields []Field
}

// Session describes the exported session.
type Session struct {
	SessionID   string
	DeviceID    string
	AppVersion  string
	Platform    string
	DeviceModel string
	OSVersion   string
	BuildType   string
	FirstSeen   time.Time
	LastSeen    time.Time
	StartedAt   *time.Time // from an exception's session_ms, when one carries it
	// Meta: the first non-empty value of these game context keys in the
	// session: gitCommit, dataSource, dataPackVersion, dataPackSha256
	// (AiTelemetry's session.json keys of the same names); and "scene", the
	// scene of the session's first row that has one.
	Meta map[string]string
}

// Input is what the repository read.
type Input struct {
	Session   Session
	Rows      []Row
	Truncated []string // sources with more rows than were read
}

// File is one file of the export.
type File struct {
	Name string
	Data []byte
}

// Result is the export: the directory name and its files (session.json first).
type Result struct {
	Dir   string
	Files []File
	// Lines per file, lines written to apm.jsonl because the client sent no
	// game clock, and properties left out of a line (a reserved key).
	Lines        map[string]int
	WithoutClock int
	DroppedProps int
	Truncated    []string
	RunIDs       []string
	ExportedAt   time.Time
	TotalBytes   int
}

var channelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,47}$`)

// reserved channel names: the export's own files.
var reservedChannels = map[string]bool{"apm": true, "custom": true, "session": true, "level": true}

// Channel splits a custom event name "<channel>.<ev>" (ApmClient.Event) into
// its AiTelemetry channel and ev; ok is false for names without a valid
// channel (they go to custom.jsonl with the whole name as ev).
func Channel(name string) (channel, ev string, ok bool) {
	i := strings.IndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	channel, ev = name[:i], name[i+1:]
	if !channelRE.MatchString(channel) || reservedChannels[channel] {
		return "", "", false
	}
	return channel, ev, true
}

// reservedKeys are the leading fields of a line: properties of these names
// are left out (counted in DroppedProps).
var reservedKeys = map[string]bool{"t": true, "frame": true, "run": true, "ev": true}

// DirName is the name of the export's directory: the session's start (UTC)
// like AiTelemetry's directories, then the session id reduced to
// [A-Za-z0-9_-] (at most 40 characters).
func DirName(s Session) string {
	start := s.FirstSeen
	if s.StartedAt != nil && !s.StartedAt.IsZero() {
		start = *s.StartedAt
	}
	var id strings.Builder
	for _, r := range s.SessionID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			id.WriteRune(r)
			if id.Len() == 40 {
				break
			}
		}
	}
	name := start.UTC().Format("2006-01-02_150405")
	if id.Len() > 0 {
		name += "_" + id.String()
	}
	return name
}

type line struct {
	file  string
	time  time.Time
	seq   uint64
	order int // input order, for a stable sort
	data  []byte
}

// Build turns the session's rows into the export's files.
func Build(in Input, exportedAt time.Time) *Result {
	res := &Result{Dir: DirName(in.Session), Lines: map[string]int{}, ExportedAt: exportedAt.UTC()}
	res.Truncated = append(res.Truncated, in.Truncated...)

	rows := append([]Row(nil), in.Rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Time.Equal(rows[j].Time) {
			return rows[i].Time.Before(rows[j].Time)
		}
		return rows[i].Seq < rows[j].Seq
	})

	// run numbers: distinct runIds in time order.
	runOf := map[string]int{}
	for _, r := range rows {
		if r.RunID != "" && runOf[r.RunID] == 0 {
			res.RunIDs = append(res.RunIDs, r.RunID)
			runOf[r.RunID] = len(res.RunIDs)
		}
	}

	lines := make([]line, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		file, data := buildLine(r, runOf[r.RunID], res)
		lines = append(lines, line{file: file, time: r.Time, seq: r.Seq, order: i, data: data})
	}

	// Files in time order of their lines, cut at MaxBytes.
	session := sessionJSON(in.Session, res)
	total := len(session) + 4096 // room for the final session.json (counts)
	buffers := map[string]*bytes.Buffer{}
	var names []string
	cut := false
	for _, l := range lines {
		if total+len(l.data)+1 > maxBytes {
			cut = true
			break
		}
		total += len(l.data) + 1
		b := buffers[l.file]
		if b == nil {
			b = &bytes.Buffer{}
			buffers[l.file] = b
			names = append(names, l.file)
		}
		b.Write(l.data)
		b.WriteByte('\n')
		res.Lines[l.file]++
	}
	if cut {
		res.Truncated = append(res.Truncated, "bytes")
	}
	sort.Strings(names)
	res.Files = append(res.Files, File{Name: FileSession, Data: sessionJSON(in.Session, res)})
	for _, n := range names {
		res.Files = append(res.Files, File{Name: n, Data: buffers[n].Bytes()})
	}
	for _, f := range res.Files {
		res.TotalBytes += len(f.Data)
	}
	return res
}

// buildLine returns the file and the JSON text of one row.
func buildLine(r *Row, run int, res *Result) (string, []byte) {
	var w writer
	hasClock := r.GameT != nil && r.Frame != nil && finite(*r.GameT)
	if r.Source == SourceCustomEvent && hasClock && !r.Aggregated {
		file, ev := FileCustom, r.Name
		if ch, e, ok := Channel(r.Name); ok {
			file, ev = ch+".jsonl", e
		}
		w.begin()
		w.key("t")
		w.raw(strconv.FormatFloat(*r.GameT, 'f', 4, 64))
		w.key("frame")
		w.raw(strconv.FormatInt(*r.Frame, 10))
		w.key("run")
		w.raw(strconv.Itoa(run))
		w.key("ev")
		w.str(ev)
		res.DroppedProps += w.props(r.Props, r.PropTypes, true)
		w.end()
		return file, w.bytes()
	}

	// apm.jsonl
	w.begin()
	if hasClock {
		w.key("t")
		w.raw(strconv.FormatFloat(*r.GameT, 'f', 4, 64))
		w.key("frame")
		w.raw(strconv.FormatInt(*r.Frame, 10))
	} else {
		res.WithoutClock++
	}
	w.key("run")
	w.raw(strconv.Itoa(run))
	w.key("ev")
	w.str("apm_" + r.Source)
	w.key("ts")
	w.str(r.Time.UTC().Format("2006-01-02T15:04:05.000Z"))
	if r.Source == SourceCustomEvent {
		w.key("name")
		w.str(r.Name)
		w.key("count")
		w.raw(strconv.FormatInt(r.Count, 10))
		if r.Aggregated {
			w.key("aggregated")
			w.raw("true")
		}
		if r.CtxMerged {
			w.key("ctxMerged")
			w.raw("true")
		}
		if len(r.Props) > 0 {
			w.key("props")
			w.begin()
			w.props(r.Props, r.PropTypes, false)
			w.end()
		}
	}
	for _, f := range r.Fields {
		w.key(f.Key)
		w.value(f.Value)
	}
	w.end()
	return FileAPM, w.bytes()
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// writer builds one JSON object; keys are written by the export (ASCII,
// known), values escaped.
type writer struct {
	buf   bytes.Buffer
	first []bool
}

func (w *writer) begin() {
	w.buf.WriteByte('{')
	w.first = append(w.first, true)
}

func (w *writer) end() {
	w.buf.WriteByte('}')
	w.first = w.first[:len(w.first)-1]
}

func (w *writer) key(k string) {
	if !w.first[len(w.first)-1] {
		w.buf.WriteByte(',')
	}
	w.first[len(w.first)-1] = false
	w.str(k)
	w.buf.WriteByte(':')
}

func (w *writer) raw(s string) { w.buf.WriteString(s) }

func (w *writer) str(s string) {
	b, _ := json.Marshal(s) // never fails for a string; invalid UTF-8 becomes U+FFFD
	w.buf.Write(b)
}

func (w *writer) value(v any) {
	switch x := v.(type) {
	case nil:
		w.raw("null")
	case string:
		w.str(x)
	case bool:
		if x {
			w.raw("true")
		} else {
			w.raw("false")
		}
	case int64:
		w.raw(strconv.FormatInt(x, 10))
	case int:
		w.raw(strconv.Itoa(x))
	case float64:
		if !finite(x) {
			w.raw("null")
		} else {
			w.raw(strconv.FormatFloat(x, 'f', -1, 64))
		}
	case []string:
		w.buf.WriteByte('[')
		for i, s := range x {
			if i > 0 {
				w.buf.WriteByte(',')
			}
			w.str(s)
		}
		w.buf.WriteByte(']')
	default:
		w.str(fmt.Sprint(x))
	}
}

func (w *writer) bytes() []byte { return w.buf.Bytes() }

// props writes the properties in key order: numbers and booleans by their
// type, a pair of numbers <k>.x and <k>.y as {"x":..,"y":..} under <k>
// (unless <k> is a property itself), everything else as strings. With
// skipReserved, keys that are a line's leading fields are left out; it
// returns how many.
func (w *writer) props(props map[string]string, types map[string]uint8, skipReserved bool) int {
	if len(props) == 0 {
		return 0
	}
	isNum := func(k string) bool {
		v, ok := props[k]
		return ok && types[k] == propNumber && validNumber(v)
	}
	keys := make([]string, 0, len(props))
	vec := map[string]bool{} // <k> written as a vector
	for k := range props {
		if strings.HasSuffix(k, ".x") {
			base := k[:len(k)-2]
			if _, clash := props[base]; !clash && base != "" && isNum(k) && isNum(base+".y") {
				vec[base] = true
				keys = append(keys, base)
				continue
			}
		}
		if strings.HasSuffix(k, ".y") {
			base := k[:len(k)-2]
			if _, clash := props[base]; !clash && base != "" && isNum(base+".x") && isNum(k) {
				continue // written with its .x
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dropped := 0
	for _, k := range keys {
		if skipReserved && reservedKeys[k] {
			dropped++
			continue
		}
		w.key(k)
		if vec[k] {
			w.begin()
			w.key("x")
			w.raw(props[k+".x"])
			w.key("y")
			w.raw(props[k+".y"])
			w.end()
			continue
		}
		v := props[k]
		switch {
		case types[k] == propNumber && validNumber(v):
			w.raw(v)
		case types[k] == propBool && (v == "true" || v == "false"):
			w.raw(v)
		default:
			w.str(v)
		}
	}
	return dropped
}

// validNumber reports whether s is a JSON number that parses to a finite
// float64 (the text is written as is).
func validNumber(s string) bool {
	if !numberRE.MatchString(s) {
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && finite(f)
}

var numberRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// TimeBase is session.json's "timeBase": the clock "t" is on.
const TimeBase = "Time.timeAsDouble (scaled game time since the player started), reported by the OZX APM SDK with each event"

func sessionJSON(s Session, res *Result) []byte {
	var w writer
	w.begin()
	started := s.FirstSeen
	if s.StartedAt != nil && !s.StartedAt.IsZero() {
		started = *s.StartedAt
	}
	w.key("startedAt")
	w.str(started.UTC().Format("2006-01-02T15:04:05.000Z"))
	if scene := s.Meta["scene"]; scene != "" {
		w.key("scene")
		w.str(scene)
	}
	w.key("gitCommit")
	if v := s.Meta["gitCommit"]; v != "" {
		w.str(v)
	} else {
		w.raw("null")
	}
	if v := s.Meta["dataSource"]; v != "" {
		w.key("dataSource")
		w.str(v)
	}
	if v := s.Meta["dataPackVersion"]; v != "" {
		w.key("dataPackVersion")
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			w.raw(strconv.FormatInt(n, 10))
		} else {
			w.str(v)
		}
	}
	if v := s.Meta["dataPackSha256"]; v != "" {
		w.key("dataPackSha256")
		w.str(v)
	}
	w.key("timeBase")
	w.str(TimeBase)
	w.key("source")
	w.str("ozx-apm")

	w.key("apm")
	w.begin()
	w.key("format")
	w.raw(strconv.Itoa(FormatVersion))
	for _, kv := range [][2]string{
		{"sessionId", s.SessionID}, {"deviceId", s.DeviceID}, {"appVersion", s.AppVersion}, {"platform", s.Platform},
		{"deviceModel", s.DeviceModel}, {"osVersion", s.OSVersion}, {"buildType", s.BuildType},
		{"firstSeen", s.FirstSeen.UTC().Format(time.RFC3339Nano)}, {"lastSeen", s.LastSeen.UTC().Format(time.RFC3339Nano)},
		{"exportedAt", res.ExportedAt.Format(time.RFC3339)},
	} {
		w.key(kv[0])
		w.str(kv[1])
	}
	w.key("runs")
	w.buf.WriteByte('[')
	for i, id := range res.RunIDs {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.begin()
		w.key("run")
		w.raw(strconv.Itoa(i + 1))
		w.key("runId")
		w.str(id)
		w.end()
	}
	w.buf.WriteByte(']')
	w.key("lines")
	w.begin()
	names := make([]string, 0, len(res.Lines))
	for n := range res.Lines {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w.key(n)
		w.raw(strconv.Itoa(res.Lines[n]))
	}
	w.end()
	w.key("linesWithoutGameClock")
	w.raw(strconv.Itoa(res.WithoutClock))
	w.key("droppedProps")
	w.raw(strconv.Itoa(res.DroppedProps))
	w.key("truncated")
	w.value(append([]string{}, res.Truncated...))
	w.end()
	w.end()

	// Indented like AiTelemetry's session.json.
	var out bytes.Buffer
	if err := json.Indent(&out, w.bytes(), "", "  "); err != nil {
		return append(w.bytes(), '\n')
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// WriteZip writes the files under res.Dir/ as a zip archive.
func WriteZip(out io.Writer, res *Result) error {
	z := zip.NewWriter(out)
	for _, f := range res.Files {
		h := &zip.FileHeader{Name: res.Dir + "/" + f.Name, Method: zip.Deflate, Modified: res.ExportedAt}
		fw, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return err
		}
	}
	return z.Close()
}
