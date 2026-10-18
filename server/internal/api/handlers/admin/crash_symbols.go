package admin

import (
	"context"
	"strconv"
	"strings"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

// Symbolication bounds of one crash in the admin API: the frames of the
// first threads are looked up (the SDK sends the crashed thread first, then
// UnityMain and main), the rest is shown as sent.
const (
	symbolicatedThreads         = 3
	symbolicatedFramesPerThread = 64
)

// symbolicateStack parses a crash stack and names its native frames from
// the uploaded symbols (s may be nil: frames as sent). It also says, per
// library of the app seen in the stack, whether symbols are uploaded. A
// store error leaves the frames as sent and is returned.
func symbolicateStack(ctx context.Context, s *symbols.Symbolizer, stack string, fb symbols.Fallback) ([]models.CrashThreadView, []models.CrashSymbolStatus, error) {
	threads := fingerprint.ParseCrashStack(stack)
	type ref struct{ t, f int }
	var frames []symbols.Frame
	var refs []ref
	for ti, t := range threads {
		if ti >= symbolicatedThreads {
			break
		}
		for fi, f := range t.Frames {
			if fi >= symbolicatedFramesPerThread {
				break
			}
			if f.Native && f.InApp {
				frames = append(frames, symbols.Frame{Module: f.ModuleName(), BuildID: f.BuildID, PC: f.PC, ReturnAddress: f.Index > 0})
				refs = append(refs, ref{ti, fi})
			}
		}
	}
	var resolved map[int]symbols.Resolved
	var status []models.CrashSymbolStatus
	var err error
	if s != nil && len(frames) > 0 {
		resolved, err = s.Resolve(ctx, frames, fb)
		if err == nil {
			status, err = symbolStatus(ctx, s, frames, fb)
		}
	}
	names := map[ref]symbols.Resolved{}
	for i, r := range resolved {
		names[refs[i]] = r
	}
	views := make([]models.CrashThreadView, 0, len(threads))
	for ti, t := range threads {
		v := models.CrashThreadView{Name: t.Name, Tid: t.Tid, Crashed: t.Crashed, Omitted: t.Omitted, Frames: make([]models.CrashFrameView, 0, len(t.Frames))}
		for fi, f := range t.Frames {
			fv := models.CrashFrameView{Index: f.Index, Native: f.Native, Module: f.Module, BuildID: f.BuildID,
				Function: f.Function, InApp: f.InApp, Raw: f.Raw, File: f.File, Line: f.Line}
			if f.Native {
				fv.PC = "0x" + strconv.FormatUint(f.PC, 16)
				fv.Offset = f.Offset
			}
			if r, ok := names[ref{ti, fi}]; ok {
				fv.Function, fv.Offset, fv.File, fv.Line = r.Name, r.Offset, r.File, int(r.Line)
				fv.Symbolicated, fv.SymbolsBy = true, r.By
			}
			v.Frames = append(v.Frames, fv)
		}
		views = append(views, v)
	}
	if status == nil {
		status = []models.CrashSymbolStatus{}
	}
	return views, status, err
}

// symbolStatus lists the app libraries of the frames and their upload.
func symbolStatus(ctx context.Context, s *symbols.Symbolizer, frames []symbols.Frame, fb symbols.Fallback) ([]models.CrashSymbolStatus, error) {
	var out []models.CrashSymbolStatus
	seen := map[string]bool{}
	for _, f := range frames {
		k := f.Module + "|" + f.BuildID
		if seen[k] {
			continue
		}
		seen[k] = true
		ref, _, found, err := s.UploadFor(ctx, f, fb)
		if err != nil {
			return out, err
		}
		st := models.CrashSymbolStatus{Module: f.Module, BuildID: f.BuildID, Found: found}
		if found {
			st.UploadID, st.UploadedAt, st.LineMap, st.MethodMap = ref.UploadID, ref.UploadedAt, ref.LineMap, ref.MethodMap
		}
		out = append(out, st)
	}
	return out, nil
}

// resolveTopFrame symbolicates only the top frame of a stack (the crash
// list): one address per group instead of the whole stack.
func resolveTopFrame(ctx context.Context, s *symbols.Symbolizer, stack, crashType string, fb symbols.Fallback) (string, bool, error) {
	threads, _, _ := symbolicateStack(ctx, nil, stack, fb)
	f := topFrameOf(threads, crashType)
	if f == nil {
		return "", false, nil
	}
	if s != nil && f.Native && f.InApp {
		pc, err := strconv.ParseUint(strings.TrimPrefix(f.PC, "0x"), 16, 64)
		if err == nil {
			got, err := s.Resolve(ctx, []symbols.Frame{{Module: fingerprint.ModuleBaseName(f.Module), BuildID: f.BuildID, PC: pc, ReturnAddress: f.Index > 0}}, fb)
			if err != nil {
				return FormatCrashFrame(*f), false, err
			}
			if r, ok := got[0]; ok {
				f.Function, f.Offset, f.File, f.Line, f.Symbolicated, f.SymbolsBy = r.Name, r.Offset, r.File, int(r.Line), true, r.By
			}
		}
	}
	return FormatCrashFrame(*f), f.Symbolicated, nil
}

// topFrameOf returns the first in-app frame of the thread that matters
// (as topFrame), nil without one.
func topFrameOf(threads []models.CrashThreadView, crashType string) *models.CrashFrameView {
	pick := func(t *models.CrashThreadView) *models.CrashFrameView {
		for i := range t.Frames {
			if t.Frames[i].InApp {
				return &t.Frames[i]
			}
		}
		return nil
	}
	if crashType == fingerprint.CrashTypeANR {
		for i := range threads {
			if threads[i].Name == "UnityMain" {
				if f := pick(&threads[i]); f != nil {
					return f
				}
				break
			}
		}
	}
	for i := range threads {
		if threads[i].Crashed {
			return pick(&threads[i])
		}
	}
	return nil
}

// topFrame describes the first in-app frame of the thread that matters
// (the crashed one; for an ANR the UnityMain thread when it has in-app
// frames): "Method (File.cs:12)", "Method", or "module+0xpc".
func topFrame(threads []models.CrashThreadView, crashType string) (string, bool) {
	pick := func(t models.CrashThreadView) (models.CrashFrameView, bool) {
		for _, f := range t.Frames {
			if f.InApp {
				return f, true
			}
		}
		return models.CrashFrameView{}, false
	}
	var f models.CrashFrameView
	ok := false
	if crashType == fingerprint.CrashTypeANR {
		for _, t := range threads {
			if t.Name == "UnityMain" {
				f, ok = pick(t)
				break
			}
		}
	}
	if !ok {
		for _, t := range threads {
			if t.Crashed {
				f, ok = pick(t)
				break
			}
		}
	}
	if !ok {
		return "", false
	}
	return FormatCrashFrame(f), f.Symbolicated
}

// FormatCrashFrame renders a frame on one line.
func FormatCrashFrame(f models.CrashFrameView) string {
	name := f.Function
	if name == "" {
		return fingerprint.ModuleBaseName(f.Module) + "+" + f.PC
	}
	if f.File != "" && f.Line > 0 {
		return name + " (" + f.File + ":" + strconv.Itoa(f.Line) + ")"
	}
	if f.File != "" {
		return name + " (" + f.File + ")"
	}
	return name
}
