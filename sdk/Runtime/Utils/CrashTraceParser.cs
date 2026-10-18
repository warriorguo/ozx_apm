using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;

namespace OzxApm.Utils
{
    /// <summary>
    /// Reduces what Android keeps about a dead process (APM-7) to what a crash
    /// report needs, on the device, so that nothing else leaves it: the
    /// tombstone of a native crash (protobuf on Android 12+, text before) and
    /// the ANR trace. Kept: the signal, its code and fault address, the abort
    /// message, and the stacks of the crashed (or blocked) thread, UnityMain
    /// (where the game's C# runs) and main, at most MaxFramesPerThread frames
    /// each. Dropped: registers, memory dumps and maps, open files, the log
    /// buffers (logcat may hold anything), the other threads.
    ///
    /// The stack is written in the server's normalized form
    /// (server/internal/fingerprint/crash.go):
    ///   Thread "UnityMain" tid=4242 crashed
    ///   #00 pc 00000000005417bc libil2cpp.so (BuildId: 0d65...)
    ///   #01 pc 000000000004e0dc /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: ...)
    ///   at com.unity3d.player.UnityPlayer.nativeRender(Native Method)
    /// The app's own libraries are named by file only (their install path
    /// has random parts); system libraries keep their path.
    ///
    /// Plain C#, no UnityEngine: tested outside Unity. Never throws.
    /// </summary>
    internal static class CrashTraceParser
    {
        internal const int MaxFramesPerThread = 64;
        internal const int MaxThreads = 3;
        internal const int MaxStackChars = 16000;
        internal const int MaxAbortMessageChars = 1000;

        internal sealed class Frame
        {
            public ulong Pc;
            public string Module = "";
            public string Function = "";
            public ulong FunctionOffset;
            public string BuildId = "";
            public string Java; // "at ..." line of a Java frame (Module etc. unused)
        }

        internal sealed class Thread
        {
            public int Tid;
            public string Name = "";
            public bool Crashed;
            public readonly List<Frame> Frames = new List<Frame>();
        }

        internal sealed class Result
        {
            public string Signal = "";
            public string SignalCode = "";
            public string FaultAddr = "";
            public string AbortMessage = "";
            public string Cause = "";
            public int Pid;
            public int Tid;
            public readonly List<Thread> Threads = new List<Thread>();
            /// <summary>The normalized stack text (empty without frames).</summary>
            public string Stack = "";
            /// <summary>"proto", "text" or "anr": what was parsed.</summary>
            public string Format = "";
        }

        // ------------------------------------------------------------------
        // Tombstones
        // ------------------------------------------------------------------

        /// <summary>
        /// Parses a tombstone: protobuf (Android 12+ getTraceInputStream of a
        /// native crash), else the text form. Null if neither yields a thread.
        /// </summary>
        internal static Result ParseTombstone(byte[] data, int length)
        {
            if (data == null || length <= 0)
                return null;
            length = Math.Min(length, data.Length);
            if (!LooksLikeText(data, length))
            {
                try
                {
                    var r = ParseProto(data, length);
                    if (r != null && r.Threads.Count > 0)
                        return Finish(r, "proto");
                }
                catch
                {
                    // Not protobuf after all: try the text form.
                }
            }
            try
            {
                var text = new UTF8Encoding(false, false).GetString(data, 0, length);
                var t = ParseTextTombstone(text);
                if (t != null && t.Threads.Count > 0)
                    return Finish(t, "text");
            }
            catch
            {
                // Unreadable: no stack.
            }
            return null;
        }

        private static bool LooksLikeText(byte[] data, int length)
        {
            // Text tombstones start with "*** *** ***" or "Build fingerprint".
            return length >= 3 && ((data[0] == '*' && data[1] == '*' && data[2] == '*') ||
                                   (data[0] == 'B' && data[1] == 'u' && data[2] == 'i'));
        }

        // tombstone.proto (system/core/debuggerd/proto): Tombstone { pid 5, tid 6,
        // signal_info 10, abort_message 14, causes 15, threads 16 (map<uint32, Thread>) },
        // Signal { number 1, name 2, code 3, code_name 4, has_fault_address 8, fault_address 9 },
        // Cause { human_readable 1 }, Thread { id 1, name 2, current_backtrace 4 },
        // BacktraceFrame { rel_pc 1, pc 2, sp 3, function_name 4, function_offset 5,
        // file_name 6, file_map_offset 7, build_id 8 }.
        internal static Result ParseProto(byte[] b, int length)
        {
            var r = new Result();
            var threads = new List<Thread>();
            int pos = 0;
            bool any = false;
            while (pos < length)
            {
                ulong key = ReadVarint(b, ref pos, length);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                if (field == 0)
                    return null;
                switch (field)
                {
                    case 5 when wire == 0: r.Pid = (int)ReadVarint(b, ref pos, length); any = true; break;
                    case 6 when wire == 0: r.Tid = (int)ReadVarint(b, ref pos, length); any = true; break;
                    case 10 when wire == 2:
                        {
                            int end = ReadLengthEnd(b, ref pos, length);
                            ParseSignal(b, pos, end, r);
                            pos = end;
                            break;
                        }
                    case 14 when wire == 2: r.AbortMessage = Cut(ReadString(b, ref pos, length), MaxAbortMessageChars); break;
                    case 15 when wire == 2:
                        {
                            int end = ReadLengthEnd(b, ref pos, length);
                            if (r.Cause.Length == 0)
                                r.Cause = Cut(ReadStringField(b, pos, end, 1), 300);
                            pos = end;
                            break;
                        }
                    case 16 when wire == 2:
                        {
                            int end = ReadLengthEnd(b, ref pos, length);
                            var t = ParseThreadEntry(b, pos, end);
                            if (t != null)
                                threads.Add(t);
                            pos = end;
                            break;
                        }
                    default:
                        Skip(b, ref pos, length, wire);
                        break;
                }
            }
            if (!any)
                return null;
            foreach (var t in threads)
                t.Crashed = t.Tid == r.Tid;
            SelectThreads(threads, r);
            return r;
        }

        private static void ParseSignal(byte[] b, int pos, int end, Result r)
        {
            int number = 0, code = 0;
            bool hasFault = false;
            ulong fault = 0;
            while (pos < end)
            {
                ulong key = ReadVarint(b, ref pos, end);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                switch (field)
                {
                    case 1 when wire == 0: number = (int)ReadVarint(b, ref pos, end); break;
                    case 2 when wire == 2: r.Signal = Cut(ReadString(b, ref pos, end), 32); break;
                    case 3 when wire == 0: code = (int)ReadVarint(b, ref pos, end); break;
                    case 4 when wire == 2: r.SignalCode = Cut(ReadString(b, ref pos, end), 32); break;
                    case 8 when wire == 0: hasFault = ReadVarint(b, ref pos, end) != 0; break;
                    case 9 when wire == 0: fault = ReadVarint(b, ref pos, end); break;
                    default: Skip(b, ref pos, end, wire); break;
                }
            }
            if (r.Signal.Length == 0 && number > 0)
                r.Signal = SignalName(number);
            if (hasFault)
                r.FaultAddr = "0x" + fault.ToString("x16", CultureInfo.InvariantCulture);
        }

        // A map<uint32, Thread> entry: key 1, value 2.
        private static Thread ParseThreadEntry(byte[] b, int pos, int end)
        {
            Thread t = null;
            int mapKey = 0;
            while (pos < end)
            {
                ulong key = ReadVarint(b, ref pos, end);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                if (field == 1 && wire == 0)
                {
                    mapKey = (int)ReadVarint(b, ref pos, end);
                }
                else if (field == 2 && wire == 2)
                {
                    int e = ReadLengthEnd(b, ref pos, end);
                    t = ParseThread(b, pos, e);
                    pos = e;
                }
                else
                {
                    Skip(b, ref pos, end, wire);
                }
            }
            if (t != null && t.Tid == 0)
                t.Tid = mapKey;
            return t;
        }

        private static Thread ParseThread(byte[] b, int pos, int end)
        {
            var t = new Thread();
            while (pos < end)
            {
                ulong key = ReadVarint(b, ref pos, end);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                switch (field)
                {
                    case 1 when wire == 0: t.Tid = (int)ReadVarint(b, ref pos, end); break;
                    case 2 when wire == 2: t.Name = Cut(ReadString(b, ref pos, end), 64); break;
                    case 4 when wire == 2:
                        {
                            int e = ReadLengthEnd(b, ref pos, end);
                            if (t.Frames.Count < MaxFramesPerThread)
                                t.Frames.Add(ParseFrame(b, pos, e));
                            pos = e;
                            break;
                        }
                    default: Skip(b, ref pos, end, wire); break;
                }
            }
            return t;
        }

        private static Frame ParseFrame(byte[] b, int pos, int end)
        {
            var f = new Frame();
            while (pos < end)
            {
                ulong key = ReadVarint(b, ref pos, end);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                switch (field)
                {
                    case 1 when wire == 0: f.Pc = ReadVarint(b, ref pos, end); break;
                    case 4 when wire == 2: f.Function = Cut(ReadString(b, ref pos, end), 256); break;
                    case 5 when wire == 0: f.FunctionOffset = ReadVarint(b, ref pos, end); break;
                    case 6 when wire == 2: f.Module = ModuleName(ReadString(b, ref pos, end)); break;
                    case 8 when wire == 2: f.BuildId = HexOnly(ReadString(b, ref pos, end)); break;
                    default: Skip(b, ref pos, end, wire); break;
                }
            }
            return f;
        }

        private static ulong ReadVarint(byte[] b, ref int pos, int end)
        {
            ulong v = 0;
            for (int shift = 0; shift < 64; shift += 7)
            {
                if (pos >= end)
                    throw new FormatException("truncated varint");
                byte x = b[pos++];
                v |= (ulong)(x & 0x7F) << shift;
                if ((x & 0x80) == 0)
                    return v;
            }
            throw new FormatException("varint too long");
        }

        private static int ReadLengthEnd(byte[] b, ref int pos, int end)
        {
            ulong n = ReadVarint(b, ref pos, end);
            if (n > (ulong)(end - pos))
                throw new FormatException("length beyond the message");
            return pos + (int)n;
        }

        private static string ReadString(byte[] b, ref int pos, int end)
        {
            int e = ReadLengthEnd(b, ref pos, end);
            string s = Encoding.UTF8.GetString(b, pos, Math.Min(e - pos, 4096));
            pos = e;
            return s;
        }

        private static string ReadStringField(byte[] b, int pos, int end, int wanted)
        {
            while (pos < end)
            {
                ulong key = ReadVarint(b, ref pos, end);
                int field = (int)(key >> 3), wire = (int)(key & 7);
                if (field == wanted && wire == 2)
                    return ReadString(b, ref pos, end);
                Skip(b, ref pos, end, wire);
            }
            return "";
        }

        private static void Skip(byte[] b, ref int pos, int end, int wire)
        {
            switch (wire)
            {
                case 0: ReadVarint(b, ref pos, end); break;
                case 1: pos += 8; break;
                case 2: pos = ReadLengthEnd(b, ref pos, end); break;
                case 5: pos += 4; break;
                default: throw new FormatException("unsupported wire type " + wire);
            }
            if (pos > end)
                throw new FormatException("field beyond the message");
        }

        // Text tombstone (Android before 12, or a text copy):
        //   pid: 123, tid: 456, name: UnityMain  >>> com.x <<<
        //   signal 11 (SIGSEGV), code 1 (SEGV_MAPERR), fault addr 0x0000000000000008
        //   Abort message: '...'
        //   backtrace:
        //         #00 pc 00000000005417bc  /data/app/.../libil2cpp.so (BuildId: ...)
        //   --- --- --- (other threads: "pid: 123, tid: 789, name: Thread-2  >>> ...", then backtrace)
        internal static Result ParseTextTombstone(string text)
        {
            var r = new Result();
            var threads = new List<Thread>();
            Thread cur = null;
            bool inBacktrace = false;
            foreach (var raw in ReadLines(text, 200000))
            {
                string line = raw.Trim();
                if (line.StartsWith("pid: ", StringComparison.Ordinal))
                {
                    cur = new Thread();
                    cur.Tid = IntAfter(line, "tid: ");
                    int n = line.IndexOf("name: ", StringComparison.Ordinal);
                    if (n >= 0)
                    {
                        string name = line.Substring(n + 6);
                        int gt = name.IndexOf("  >>>", StringComparison.Ordinal);
                        cur.Name = Cut((gt >= 0 ? name.Substring(0, gt) : name).Trim(), 64);
                    }
                    if (threads.Count == 0)
                    {
                        r.Pid = IntAfter(line, "pid: ");
                        r.Tid = cur.Tid;
                        cur.Crashed = true;
                    }
                    threads.Add(cur);
                    inBacktrace = false;
                    continue;
                }
                if (line.StartsWith("signal ", StringComparison.Ordinal) && r.Signal.Length == 0)
                {
                    r.Signal = Between(line, "(", ")");
                    int code = line.IndexOf("code ", StringComparison.Ordinal);
                    if (code >= 0)
                        r.SignalCode = Between(line.Substring(code), "(", ")");
                    int fa = line.IndexOf("fault addr ", StringComparison.Ordinal);
                    if (fa >= 0)
                        r.FaultAddr = Cut(line.Substring(fa + 11).Trim(), 32);
                    continue;
                }
                if (line.StartsWith("Abort message: ", StringComparison.Ordinal))
                {
                    string m = line.Substring(15).Trim();
                    if (m.Length >= 2 && m[0] == '\'' && m[m.Length - 1] == '\'')
                        m = m.Substring(1, m.Length - 2);
                    r.AbortMessage = Cut(m, MaxAbortMessageChars);
                    continue;
                }
                if (line.StartsWith("Cause: ", StringComparison.Ordinal) && r.Cause.Length == 0)
                {
                    r.Cause = Cut(line.Substring(7), 300);
                    continue;
                }
                if (line == "backtrace:")
                {
                    inBacktrace = true;
                    continue;
                }
                if (line.StartsWith("#", StringComparison.Ordinal) && inBacktrace && cur != null)
                {
                    var f = ParseFrameLine(line);
                    if (f != null && cur.Frames.Count < MaxFramesPerThread)
                        cur.Frames.Add(f);
                    continue;
                }
                // Another section ("stack:", "memory map ...:", "--- --- ---") ends the backtrace.
                if (line.StartsWith("---", StringComparison.Ordinal) || line.EndsWith(":", StringComparison.Ordinal))
                    inBacktrace = false;
            }
            SelectThreads(threads, r);
            return r;
        }

        // ------------------------------------------------------------------
        // ANR traces
        // ------------------------------------------------------------------

        /// <summary>
        /// Reduces an ANR trace (Android 11+ getTraceInputStream of an ANR: the
        /// dump of every thread) to the main thread (blocked: marked crashed)
        /// and UnityMain. Reads at most maxChars characters from the reader.
        /// </summary>
        internal static Result ParseAnr(TextReader reader, int maxChars)
        {
            var r = new Result();
            var threads = new List<Thread>();
            Thread cur = null;
            int read = 0;
            string raw;
            while ((raw = reader.ReadLine()) != null)
            {
                read += raw.Length + 1;
                if (read > maxChars)
                    break;
                if (raw.StartsWith("\"", StringComparison.Ordinal))
                {
                    // "main" prio=5 tid=1 Sleeping
                    int q = raw.IndexOf('"', 1);
                    string name = q > 0 ? raw.Substring(1, q - 1) : "";
                    if (name == "main" || name == "UnityMain")
                    {
                        cur = new Thread { Name = name, Tid = IntAfter(raw, "tid="), Crashed = name == "main" };
                        threads.Add(cur);
                    }
                    else
                    {
                        cur = null;
                    }
                    continue;
                }
                if (cur == null)
                    continue;
                string line = raw.Trim();
                if (line.StartsWith("| sysTid=", StringComparison.Ordinal) || line.StartsWith("| ", StringComparison.Ordinal) && line.Contains("sysTid="))
                {
                    int t = IntAfter(line, "sysTid=");
                    if (t > 0)
                        cur.Tid = t;
                    continue;
                }
                if (cur.Frames.Count >= MaxFramesPerThread)
                    continue;
                if (line.StartsWith("native: ", StringComparison.Ordinal))
                {
                    var f = ParseFrameLine(line.Substring(8));
                    if (f != null)
                        cur.Frames.Add(f);
                }
                else if (line.StartsWith("at ", StringComparison.Ordinal))
                {
                    cur.Frames.Add(new Frame { Java = Cut(line, 300) });
                }
            }
            if (threads.Count == 0)
                return null;
            SelectThreads(threads, r);
            return Finish(r, "anr");
        }

        // ------------------------------------------------------------------
        // Shared
        // ------------------------------------------------------------------

        // "#00 pc 000000000004e0dc  /apex/.../libc.so (abort+164) (BuildId: 2c4f)"
        internal static Frame ParseFrameLine(string line)
        {
            line = line.Trim();
            if (!line.StartsWith("#", StringComparison.Ordinal))
                return null;
            int pc = line.IndexOf(" pc ", StringComparison.Ordinal);
            if (pc < 0)
                return null;
            string rest = line.Substring(pc + 4).TrimStart();
            int sp = 0;
            while (sp < rest.Length && IsHex(rest[sp]))
                sp++;
            if (sp == 0 || sp > 16 || !ulong.TryParse(rest.Substring(0, sp), NumberStyles.AllowHexSpecifier, CultureInfo.InvariantCulture, out ulong value))
                return null;
            var f = new Frame { Pc = value };
            rest = rest.Substring(sp).Trim();
            // Groups at the end, right to left.
            while (rest.EndsWith(")", StringComparison.Ordinal))
            {
                int open = GroupOpen(rest);
                if (open < 0)
                    break;
                string g = rest.Substring(open + 1, rest.Length - open - 2);
                if (g.StartsWith("BuildId: ", StringComparison.Ordinal))
                {
                    f.BuildId = HexOnly(g.Substring(9));
                }
                else if (g.StartsWith("offset 0x", StringComparison.Ordinal) || g.StartsWith("deleted", StringComparison.Ordinal))
                {
                    // Not needed: pc is relative to the library.
                }
                else if (f.Function.Length == 0)
                {
                    int plus = g.LastIndexOf('+');
                    if (plus > 0 && ulong.TryParse(g.Substring(plus + 1), NumberStyles.None, CultureInfo.InvariantCulture, out ulong off))
                    {
                        f.Function = Cut(g.Substring(0, plus), 256);
                        f.FunctionOffset = off;
                    }
                    else
                    {
                        f.Function = Cut(g, 256);
                    }
                }
                rest = rest.Substring(0, open).TrimEnd();
            }
            f.Module = ModuleName(rest);
            return f;
        }

        private static int GroupOpen(string s)
        {
            int depth = 0;
            for (int i = s.Length - 1; i >= 0; i--)
            {
                if (s[i] == ')')
                {
                    depth++;
                }
                else if (s[i] == '(')
                {
                    depth--;
                    if (depth == 0)
                        return i == 0 || s[i - 1] == ' ' ? i : -1;
                }
            }
            return -1;
        }

        /// <summary>
        /// A library path as sent: system paths as they are; the app's own
        /// libraries (under /data/, or inside an APK) by file name.
        /// </summary>
        internal static string ModuleName(string path)
        {
            path = (path ?? "").Trim();
            if (path.Length == 0)
                return "";
            bool system = path.StartsWith("/system/", StringComparison.Ordinal) || path.StartsWith("/apex/", StringComparison.Ordinal) ||
                          path.StartsWith("/vendor/", StringComparison.Ordinal) || path.StartsWith("/product/", StringComparison.Ordinal) ||
                          path.StartsWith("/system_ext/", StringComparison.Ordinal) || path.StartsWith("/odm/", StringComparison.Ordinal) ||
                          path.StartsWith("[", StringComparison.Ordinal);
            if (!system || path.Contains(".apk!"))
            {
                int bang = path.LastIndexOf('!');
                if (bang >= 0)
                    path = path.Substring(bang + 1);
                int slash = path.LastIndexOf('/');
                if (slash >= 0)
                    path = path.Substring(slash + 1);
            }
            return Cut(path, 200);
        }

        // Keeps the crashed thread, UnityMain and main (in that order, no duplicates).
        private static void SelectThreads(List<Thread> all, Result r)
        {
            Thread crashed = null, unity = null, main = null;
            foreach (var t in all)
            {
                if (t.Crashed && crashed == null)
                    crashed = t;
                else if (t.Name == "UnityMain" && unity == null)
                    unity = t;
                else if ((t.Name == "main" || (r.Pid != 0 && t.Tid == r.Pid)) && main == null)
                    main = t;
            }
            if (crashed == null && all.Count > 0)
            {
                crashed = all[0];
                crashed.Crashed = true;
                if (ReferenceEquals(unity, crashed)) unity = null;
                if (ReferenceEquals(main, crashed)) main = null;
            }
            foreach (var t in new[] { crashed, unity, main })
            {
                if (t != null && r.Threads.Count < MaxThreads)
                    r.Threads.Add(t);
            }
        }

        private static Result Finish(Result r, string format)
        {
            r.Format = format;
            var sb = new StringBuilder(2048);
            foreach (var t in r.Threads)
            {
                if (sb.Length >= MaxStackChars)
                    break;
                sb.Append("Thread \"").Append(Clean(t.Name)).Append("\" tid=").Append(t.Tid.ToString(CultureInfo.InvariantCulture));
                if (t.Crashed)
                    sb.Append(" crashed");
                sb.Append('\n');
                for (int i = 0; i < t.Frames.Count && sb.Length < MaxStackChars; i++)
                {
                    var f = t.Frames[i];
                    if (f.Java != null)
                    {
                        sb.Append(Clean(f.Java)).Append('\n');
                        continue;
                    }
                    sb.Append('#').Append(i.ToString("00", CultureInfo.InvariantCulture)).Append(" pc ")
                      .Append(f.Pc.ToString("x16", CultureInfo.InvariantCulture)).Append(' ').Append(Clean(f.Module));
                    if (f.Function.Length > 0)
                    {
                        sb.Append(" (").Append(Clean(f.Function));
                        if (f.FunctionOffset > 0)
                            sb.Append('+').Append(f.FunctionOffset.ToString(CultureInfo.InvariantCulture));
                        sb.Append(')');
                    }
                    if (f.BuildId.Length > 0)
                        sb.Append(" (BuildId: ").Append(f.BuildId).Append(')');
                    sb.Append('\n');
                }
            }
            if (sb.Length > MaxStackChars)
                sb.Length = MaxStackChars;
            r.Stack = sb.ToString();
            return r;
        }

        // No line breaks or control characters inside a line of the stack.
        private static string Clean(string s)
        {
            if (string.IsNullOrEmpty(s))
                return "";
            for (int i = 0; i < s.Length; i++)
            {
                if (s[i] < ' ')
                {
                    var b = new StringBuilder(s.Length);
                    foreach (char c in s)
                        b.Append(c < ' ' ? ' ' : c);
                    return b.ToString();
                }
            }
            return s;
        }

        private static IEnumerable<string> ReadLines(string text, int maxLines)
        {
            int start = 0, n = 0;
            while (start < text.Length && n < maxLines)
            {
                int nl = text.IndexOf('\n', start);
                if (nl < 0)
                    nl = text.Length;
                yield return text.Substring(start, nl - start).TrimEnd('\r');
                start = nl + 1;
                n++;
            }
        }

        private static int IntAfter(string s, string marker)
        {
            int i = s.IndexOf(marker, StringComparison.Ordinal);
            if (i < 0)
                return 0;
            i += marker.Length;
            int v = 0;
            while (i < s.Length && s[i] >= '0' && s[i] <= '9' && v < 100000000)
                v = v * 10 + (s[i++] - '0');
            return v;
        }

        private static string Between(string s, string open, string close)
        {
            int a = s.IndexOf(open, StringComparison.Ordinal);
            if (a < 0)
                return "";
            int b = s.IndexOf(close, a + open.Length, StringComparison.Ordinal);
            return b < 0 ? "" : Cut(s.Substring(a + open.Length, b - a - open.Length), 32);
        }

        private static bool IsHex(char c) => (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');

        private static string HexOnly(string s)
        {
            s = (s ?? "").Trim().ToLowerInvariant();
            if (s.Length == 0 || s.Length > 128)
                return "";
            foreach (char c in s)
            {
                if (!IsHex(c))
                    return "";
            }
            return s;
        }

        internal static string Cut(string s, int max)
        {
            if (s == null)
                return "";
            if (s.Length <= max)
                return s;
            int n = max;
            if (n > 0 && char.IsHighSurrogate(s[n - 1]))
                n--;
            return s.Substring(0, n);
        }

        /// <summary>The name of a Linux signal number (Android, arm/arm64/x86).</summary>
        internal static string SignalName(int n)
        {
            switch (n)
            {
                case 4: return "SIGILL";
                case 5: return "SIGTRAP";
                case 6: return "SIGABRT";
                case 7: return "SIGBUS";
                case 8: return "SIGFPE";
                case 9: return "SIGKILL";
                case 11: return "SIGSEGV";
                case 13: return "SIGPIPE";
                case 15: return "SIGTERM";
                case 31: return "SIGSYS";
                default: return "SIG" + n.ToString(CultureInfo.InvariantCulture);
            }
        }
    }
}
