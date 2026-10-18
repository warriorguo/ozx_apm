package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

type stubSymbolStore struct {
	ref  symbols.UploadRef
	fail error
}

func (s *stubSymbolStore) LatestSymbolUpload(_ context.Context, buildID string) (symbols.UploadRef, bool, error) {
	if s.fail != nil {
		return symbols.UploadRef{}, false, s.fail
	}
	return s.ref, buildID == s.ref.BuildID, nil
}

func (s *stubSymbolStore) SymbolUploadsForVersion(context.Context, string, string) ([]symbols.UploadRef, error) {
	return nil, nil
}

func (s *stubSymbolStore) LookupSymbols(_ context.Context, _ symbols.UploadRef, addrs []uint64) (map[uint64]symbols.Hit, error) {
	out := map[uint64]symbols.Hit{}
	for _, a := range addrs {
		switch {
		case a >= 0x5417bc-0x20 && a < 0x5417bc+0x20:
			out[a] = symbols.Hit{Func: symbols.Func{Addr: 0x5417bc - 0x20, Size: 0x40, Name: "ApmTestDriver.DeepC(System.Int32)"},
				Line: symbols.Line{Addr: 0x5417bc - 0x8, File: "Assets/Scripts/ApmTestDriver.cs", Line: 812}}
		case a >= 0x541400 && a < 0x541500:
			out[a] = symbols.Hit{Func: symbols.Func{Addr: 0x541400, Size: 0x100, Name: "ApmTestDriver.DeepB()"}}
		}
	}
	return out, nil
}

const adminCrashStack = `Thread "UnityMain" tid=4242 crashed
#00 pc 00000000005417bc libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#01 pc 0000000000541470 libil2cpp.so (BuildId: 0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077)
#02 pc 0000000000a1b2c4 libunity.so (BuildId: 1111111111111111)
#03 pc 00000000000c8d2c /apex/com.android.runtime/lib64/bionic/libc.so (__pthread_start(void*)+208) (BuildId: 9c7a)
Thread "main" tid=4200
at com.unity3d.player.UnityPlayer.nativeRender(Native Method)
`

func TestSymbolicateStack(t *testing.T) {
	store := &stubSymbolStore{ref: symbols.UploadRef{BuildID: "0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077", UploadID: "u1", Module: "libil2cpp.so", LineMap: true}}
	s := symbols.NewSymbolizer(store, 100)
	threads, status, err := symbolicateStack(context.Background(), s, adminCrashStack, symbols.Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || !threads[0].Crashed || len(threads[0].Frames) != 4 {
		t.Fatalf("threads %+v", threads)
	}
	f0, f1, f2, f3 := threads[0].Frames[0], threads[0].Frames[1], threads[0].Frames[2], threads[0].Frames[3]
	if !f0.Symbolicated || f0.Function != "ApmTestDriver.DeepC(System.Int32)" || f0.File != "Assets/Scripts/ApmTestDriver.cs" || f0.Line != 812 || f0.Offset != 0x20 || f0.PC != "0x5417bc" {
		t.Errorf("frame 0 %+v", f0)
	}
	if !f1.Symbolicated || f1.Function != "ApmTestDriver.DeepB()" || f1.File != "" {
		t.Errorf("frame 1 %+v", f1)
	}
	if f2.Symbolicated || f2.Function != "" || !f2.InApp {
		t.Errorf("libunity without symbols %+v", f2)
	}
	if f3.Symbolicated || f3.Function != "__pthread_start(void*)" || f3.InApp || f3.Offset != 208 {
		t.Errorf("system frame %+v", f3)
	}
	if len(status) != 2 || !status[0].Found || status[0].Module != "libil2cpp.so" || !status[0].LineMap || status[1].Found || status[1].Module != "libunity.so" {
		t.Errorf("status %+v", status)
	}
	top, sym := topFrame(threads, "native")
	if top != "ApmTestDriver.DeepC(System.Int32) (Assets/Scripts/ApmTestDriver.cs:812)" || !sym {
		t.Errorf("top frame %q %v", top, sym)
	}

	// Without a symbolizer (or on a store error): the frames as sent.
	threads, status, err = symbolicateStack(context.Background(), nil, adminCrashStack, symbols.Fallback{})
	if err != nil || threads[0].Frames[0].Symbolicated || len(status) != 0 {
		t.Fatalf("no symbolizer: %+v %v", threads[0].Frames[0], err)
	}
	if top, sym := topFrame(threads, "native"); top != "libil2cpp.so+0x5417bc" || sym {
		t.Errorf("top frame by address %q", top)
	}
	store.fail = errors.New("down")
	if _, _, err := symbolicateStack(context.Background(), symbols.NewSymbolizer(store, 10), adminCrashStack, symbols.Fallback{}); err == nil {
		t.Error("store error not returned")
	}
	// Game text in a frame is data: kept as text (the UI escapes it).
	threads, _, _ = symbolicateStack(context.Background(), nil, "#00 pc 0000000000001000 lib<script>.so (<img onerror=x>+1)\n", symbols.Fallback{})
	if threads[0].Frames[0].Function != "<img onerror=x>" {
		t.Errorf("%+v", threads[0].Frames[0])
	}
}
