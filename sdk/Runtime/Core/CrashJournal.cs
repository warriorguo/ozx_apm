using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Threading;
using OzxApm.Collectors;

namespace OzxApm.Core
{
    /// <summary>
    /// What the crash journal records besides the time line and the game
    /// context: set once at Initialize, and by ApmClient on the main thread
    /// (scene, pause). Read by the journal's thread; every field is a single
    /// reference or value, so a read is never torn, at worst one write late.
    /// </summary>
    internal sealed class JournalInputs
    {
        public int Pid;
        public long ProcessStartMs;
        public string AppVersion = "";
        public string BuildType = "";
        public string Platform = "";
        public string DeviceModel = "";
        public string OsVersion = "";
        public string Gpu = "";
        public string RamClass = "";
        public string Abi = "";
        public volatile string NetType = "";
        public volatile string Scene = "";
        public volatile bool Paused;
        /// <summary>The current session (id and start); DeviceId reads the current device id.</summary>
        public SessionClock Clock;
        public Func<string> DeviceId;
        public Timeline Timeline;
        public PerformanceCollector Perf;
    }

    /// <summary>
    /// The crash journal (APM-7): a small file per process, rewritten when what
    /// a crash report needs changes (breadcrumbs, recent warnings and errors,
    /// game context, scene, foreground/background, the last performance
    /// figures) and every HeartbeatMs otherwise, so that after a native crash,
    /// an ANR or a kill the next launch knows the session, its context and what
    /// happened before (CrashReporter).
    ///
    /// Cost: recording code only calls NotifyChanged (a volatile read and an
    /// interlocked exchange; a kernel signal only when the writer is asleep).
    /// The file is written on the journal's own thread, at most every
    /// MinIntervalMs (changes in between are coalesced), into reused buffers:
    /// no allocation per write once the buffers have their size (the time
    /// line is formatted straight into the buffer), no fsync. The OS page
    /// cache keeps what was written when the process dies, so the write is
    /// enough; only a power loss can lose it. Two files are written in turn,
    /// each ending with a checksum, so a process killed in the middle of a
    /// write leaves the previous one intact.
    ///
    /// Format (UTF-8 text, one "key=value" per line, values escaped):
    ///   OZXJ 1 / pid / start / written / seq / session / session_start /
    ///   device / app_version / build_type / platform / device_model /
    ///   os_version / gpu / ram_class / net_type / abi / scene / fg /
    ///   fps / frame_ms / frame_ms_max / mem_mb / gc_count /
    ///   ctx.&lt;key&gt; (one per context entry) / crumb (one per time line line, oldest first) /
    ///   end=&lt;FNV-1a 64 of the bytes before this line&gt;
    /// </summary>
    internal sealed class CrashJournal
    {
        internal const string DirectoryName = "ozx_apm_crash";
        internal const string FilePrefix = "j-";
        internal const string FileSuffix = ".txt";
        internal const int MinIntervalMs = 100;
        internal const int HeartbeatMs = 5000;
        // The time line a crash carries: like an exception's first occurrence.
        internal const int JournalBreadcrumbs = 20;
        internal const int JournalLogLines = 10;
        internal const int JournalBreadcrumbLength = 200;
        internal const int JournalLogLength = 300;
        internal const int MaxFileBytes = 64 * 1024;
        internal const string Magic = "OZXJ 1";

        private static volatile CrashJournal s_active;

        /// <summary>
        /// Something the journal records changed. Any thread, never blocks:
        /// the first change after a write wakes the writer; the rest only set
        /// the flag it clears.
        /// </summary>
        internal static void NotifyChanged()
        {
            var j = s_active;
            if (j != null)
                j.MarkDirty();
        }

        private readonly string _dir;
        private readonly JournalInputs _in;
        private readonly string[] _paths = new string[2];
        private readonly FileStream[] _streams = new FileStream[2];
        private readonly AutoResetEvent _wake = new AutoResetEvent(false);
        private readonly object _writeLock = new object();
        private readonly StringBuilder _text = new StringBuilder(8 * 1024);
        private readonly UTF8Encoding _utf8 = new UTF8Encoding(false);
        private char[] _chars = new char[8 * 1024];
        private byte[] _bytes = new byte[16 * 1024];
        private static readonly Action<StringBuilder, StringBuilder> s_appendEscaped = AppendEscaped;

        private Thread _thread;
        private volatile bool _stop;
        private volatile bool _sleeping;
        private int _dirty;
        private int _slot;
        private long _seq;
        private long _lastWriteMs;

        /// <summary>Writes so far (stats, tests).</summary>
        internal long Writes;
        /// <summary>Writes that failed (disk full, permissions); the journal keeps trying.</summary>
        internal long WriteFailures;
        /// <summary>Duration of the last write on the journal thread, in Stopwatch ticks.</summary>
        internal long LastWriteTicks;

        internal CrashJournal(string dir, JournalInputs inputs)
        {
            _dir = dir;
            _in = inputs;
            string stem = FilePrefix + inputs.Pid + "-" + inputs.ProcessStartMs + "-";
            _paths[0] = Path.Combine(dir, stem + "a" + FileSuffix);
            _paths[1] = Path.Combine(dir, stem + "b" + FileSuffix);
        }

        internal string Directory => _dir;

        /// <summary>The two files of this process.</summary>
        internal string[] Paths => _paths;

        internal bool IsRunning => _thread != null;

        /// <summary>
        /// Starts the writer thread, which writes the journal at once (no file
        /// I/O on the calling thread). Without a thread (tests, or when one
        /// cannot be created) the journal is written here and then by WriteNow only.
        /// </summary>
        internal void Start(bool useThread = true)
        {
            if (_thread != null)
                return;
            _stop = false;
            s_active = this;
            Interlocked.Exchange(ref _dirty, 1);
            if (useThread)
            {
                try
                {
                    var t = new Thread(Loop) { IsBackground = true, Name = "OzxApm.Journal" };
                    _thread = t;
                    t.Start();
                    return;
                }
                catch (Exception ex)
                {
                    _thread = null;
                    ApmClient.ReportInternalError("CrashJournal.Start", ex);
                }
            }
            WriteNow();
        }

        /// <summary>
        /// Stops writing. deleteFiles: this process's journal is removed (a
        /// clean exit, or consent withdrawn), so the next launch reports nothing for it.
        /// </summary>
        internal void Stop(bool deleteFiles)
        {
            if (ReferenceEquals(s_active, this))
                s_active = null;
            _stop = true;
            _wake.Set();
            var t = _thread;
            _thread = null;
            if (t != null && !ReferenceEquals(Thread.CurrentThread, t))
            {
                try
                {
                    t.Join(500);
                }
                catch
                {
                    // Joining is best effort.
                }
            }
            lock (_writeLock)
            {
                for (int i = 0; i < 2; i++)
                {
                    try
                    {
                        _streams[i]?.Dispose();
                    }
                    catch
                    {
                        // Closing never throws into the game.
                    }
                    _streams[i] = null;
                    if (deleteFiles)
                    {
                        try
                        {
                            File.Delete(_paths[i]);
                        }
                        catch
                        {
                            // Gone already, or not deletable: the next launch reports it as unclean at worst.
                        }
                    }
                }
            }
        }

        private void MarkDirty()
        {
            if (Interlocked.Exchange(ref _dirty, 1) == 0 && _sleeping)
                _wake.Set();
        }

        private void Loop()
        {
            while (!_stop)
            {
                _sleeping = true;
                // A change between the last write and here is not lost: the
                // flag is checked before waiting.
                if (Volatile.Read(ref _dirty) == 0)
                    _wake.WaitOne(HeartbeatMs);
                _sleeping = false;
                if (_stop)
                    break;
                long since = NowMs() - _lastWriteMs;
                if (since >= 0 && since < MinIntervalMs)
                {
                    // Coalesce a burst (warning spam, a context set key by key).
                    Thread.Sleep((int)(MinIntervalMs - since));
                    if (_stop)
                        break;
                }
                WriteNow();
            }
        }

        private static long NowMs() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

        /// <summary>Builds and writes the journal now (the writer thread; tests).</summary>
        internal void WriteNow()
        {
            lock (_writeLock)
            {
                if (_stop)
                    return; // stopped: never recreate the files (consent withdrawn, clean exit)
                long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
                Interlocked.Exchange(ref _dirty, 0);
                try
                {
                    int n = Build();
                    WriteSlot(n);
                    Writes++;
                }
                catch (Exception ex)
                {
                    if (Interlocked.Increment(ref WriteFailures) <= 3)
                        ApmClient.ReportInternalError("CrashJournal.Write", ex);
                }
                _lastWriteMs = NowMs();
                LastWriteTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
            }
        }

        /// <summary>The text of the next write (tests).</summary>
        internal string BuildText()
        {
            lock (_writeLock)
            {
                int n = Build();
                return _utf8.GetString(_bytes, 0, n);
            }
        }

        // Fills _bytes with the journal; returns its length.
        private int Build()
        {
            var sb = _text;
            sb.Length = 0;
            var input = _in;
            sb.Append(Magic).Append('\n');
            Num("pid", input.Pid);
            Num("start", input.ProcessStartMs);
            Num("written", NowMs());
            Num("seq", ++_seq);
            var clock = input.Clock;
            if (clock != null)
            {
                Kv("session", clock.SessionId);
                Num("session_start", clock.StartUnixMs);
            }
            Kv("device", input.DeviceId?.Invoke());
            Kv("app_version", input.AppVersion);
            Kv("build_type", input.BuildType);
            Kv("platform", input.Platform);
            Kv("device_model", input.DeviceModel);
            Kv("os_version", input.OsVersion);
            Kv("gpu", input.Gpu);
            Kv("ram_class", input.RamClass);
            Kv("net_type", input.NetType);
            Kv("abi", input.Abi);
            Kv("scene", input.Scene);
            Num("fg", input.Paused ? 0 : 1);
            var perf = input.Perf;
            if (perf != null && perf.LastFps > 0)
            {
                Tenths("fps", perf.LastFps);
                Tenths("frame_ms", perf.LastFrameMs);
                Tenths("frame_ms_max", perf.LastMaxFrameMs);
                Tenths("mem_mb", perf.LastMemMb);
                Num("gc_count", perf.LastGcCount);
            }
            var ctx = GameContext.Current;
            for (int i = 0; i < ctx.Count; i++)
            {
                // Keys are [A-Za-z0-9_.-]: no escaping needed.
                sb.Append("ctx.").Append(ctx.GetKey(i)).Append('=');
                AppendEscaped(sb, ctx.GetValue(i));
                sb.Append('\n');
            }
            input.Timeline?.AppendSnapshot(sb, "crumb=", s_appendEscaped,
                JournalBreadcrumbs, JournalLogLines, JournalBreadcrumbLength, JournalLogLength);

            // Encode, then the checksum line over the encoded bytes.
            int chars = sb.Length;
            if (_chars.Length < chars)
                _chars = new char[Math.Max(chars, _chars.Length * 2)];
            sb.CopyTo(0, _chars, 0, chars);
            int need = _utf8.GetMaxByteCount(chars) + 32;
            if (_bytes.Length < need)
                _bytes = new byte[Math.Max(need, _bytes.Length * 2)];
            int n = _utf8.GetBytes(_chars, 0, chars, _bytes, 0);
            ulong h = Fnv(_bytes, 0, n);
            n = AppendAscii(_bytes, n, "end=");
            for (int shift = 60; shift >= 0; shift -= 4)
                _bytes[n++] = (byte)Hex((int)(h >> shift) & 0xF);
            _bytes[n++] = (byte)'\n';
            return n;
        }

        private void Kv(string key, string value)
        {
            _text.Append(key).Append('=');
            if (value != null)
                AppendEscaped(_text, value);
            _text.Append('\n');
        }

        private void Num(string key, long value)
        {
            _text.Append(key).Append('=');
            Timeline.AppendDigits(_text, value);
            _text.Append('\n');
        }

        // A float with one decimal, without allocating.
        private void Tenths(string key, float value)
        {
            if (float.IsNaN(value) || float.IsInfinity(value))
                value = 0;
            long t = (long)Math.Round(value * 10.0);
            _text.Append(key).Append('=');
            if (t < 0)
            {
                _text.Append('-');
                t = -t;
            }
            Timeline.AppendDigits(_text, t / 10);
            _text.Append('.').Append((char)('0' + t % 10)).Append('\n');
        }

        private static int AppendAscii(byte[] b, int n, string s)
        {
            for (int i = 0; i < s.Length; i++)
                b[n++] = (byte)s[i];
            return n;
        }

        private static char Hex(int v) => (char)(v < 10 ? '0' + v : 'a' + v - 10);

        internal static ulong Fnv(byte[] b, int offset, int count)
        {
            ulong h = 14695981039346656037UL;
            for (int i = offset; i < offset + count; i++)
            {
                h ^= b[i];
                h *= 1099511628211UL;
            }
            return h;
        }

        private static void AppendEscaped(StringBuilder dst, StringBuilder src)
        {
            for (int i = 0; i < src.Length; i++)
                AppendEscapedChar(dst, src[i]);
        }

        internal static void AppendEscaped(StringBuilder dst, string s)
        {
            for (int i = 0; i < s.Length; i++)
                AppendEscapedChar(dst, s[i]);
        }

        private static void AppendEscapedChar(StringBuilder dst, char c)
        {
            switch (c)
            {
                case '\\': dst.Append("\\\\"); break;
                case '\n': dst.Append("\\n"); break;
                case '\r': dst.Append("\\r"); break;
                default: dst.Append(c); break;
            }
        }

        private void WriteSlot(int n)
        {
            int slot = _slot;
            _slot = 1 - slot;
            var fs = _streams[slot];
            if (fs == null)
            {
                System.IO.Directory.CreateDirectory(_dir);
                // bufferSize 1: every Write goes straight to the OS (the page
                // cache), nothing waits in a managed buffer when the process dies.
                fs = new FileStream(_paths[slot], FileMode.OpenOrCreate, FileAccess.Write, FileShare.ReadWrite | FileShare.Delete, 1, FileOptions.None);
                _streams[slot] = fs;
            }
            fs.Position = 0;
            fs.Write(_bytes, 0, n);
            fs.SetLength(n);
        }

        // ------------------------------------------------------------------
        // Reading the journals of earlier processes (CrashReporter)
        // ------------------------------------------------------------------

        /// <summary>
        /// The journals in dir of processes other than (pid, startMs): the
        /// valid file of each with the highest seq, newest process first.
        /// Invalid or unreadable files are listed in invalid (to be deleted).
        /// </summary>
        internal static List<JournalRecord> ReadPrevious(string dir, int pid, long startMs, List<string> invalid)
        {
            var byProcess = new Dictionary<string, JournalRecord>(StringComparer.Ordinal);
            if (!System.IO.Directory.Exists(dir))
                return new List<JournalRecord>();
            string own = FilePrefix + pid + "-" + startMs + "-";
            foreach (var path in System.IO.Directory.GetFiles(dir, FilePrefix + "*" + FileSuffix))
            {
                string name = Path.GetFileName(path);
                if (name.StartsWith(own, StringComparison.Ordinal))
                    continue;
                // j-<pid>-<start>-<slot>.txt
                string stem = name.Length > 6 ? name.Substring(0, name.Length - FileSuffix.Length - 2) : name;
                JournalRecord rec = null;
                try
                {
                    var info = new FileInfo(path);
                    if (info.Length > 0 && info.Length <= MaxFileBytes)
                        rec = JournalRecord.Parse(File.ReadAllBytes(path));
                }
                catch
                {
                    rec = null;
                }
                if (rec == null)
                {
                    invalid?.Add(path);
                    continue;
                }
                rec.Files.Add(path);
                if (byProcess.TryGetValue(stem, out var other))
                {
                    var keep = rec.Seq > other.Seq ? rec : other;
                    var drop = ReferenceEquals(keep, rec) ? other : rec;
                    keep.Files.AddRange(drop.Files);
                    byProcess[stem] = keep;
                }
                else
                {
                    byProcess[stem] = rec;
                }
            }
            var list = new List<JournalRecord>(byProcess.Values);
            list.Sort((a, b) => b.StartMs.CompareTo(a.StartMs));
            // A slot that failed validation next to a valid one belongs to it.
            if (invalid != null)
            {
                for (int i = invalid.Count - 1; i >= 0; i--)
                {
                    string name = Path.GetFileName(invalid[i]);
                    foreach (var r in list)
                    {
                        if (name.StartsWith(FilePrefix + r.Pid + "-" + r.StartMs + "-", StringComparison.Ordinal))
                        {
                            r.Files.Add(invalid[i]);
                            invalid.RemoveAt(i);
                            break;
                        }
                    }
                }
            }
            return list;
        }

        /// <summary>Deletes every file of the journal directory and the directory (consent denied).</summary>
        internal static void DeleteAll(string dir)
        {
            try
            {
                if (System.IO.Directory.Exists(dir))
                    System.IO.Directory.Delete(dir, true);
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashJournal.DeleteAll", ex);
            }
        }
    }

    /// <summary>A journal of an earlier process, as read back (CrashJournal format).</summary>
    internal sealed class JournalRecord
    {
        public int Pid;
        public long StartMs;
        public long WrittenMs;
        public long Seq;
        public string SessionId = "";
        public long SessionStartMs;
        public string DeviceId = "";
        public string AppVersion = "";
        public string BuildType = "";
        public string Platform = "";
        public string DeviceModel = "";
        public string OsVersion = "";
        public string Gpu = "";
        public string RamClass = "";
        public string NetType = "";
        public string Abi = "";
        public string Scene = "";
        public bool Foreground;
        public readonly List<string> PerfKeys = new List<string>();
        public readonly List<string> PerfValues = new List<string>();
        public readonly List<string> ContextKeys = new List<string>();
        public readonly List<string> ContextValues = new List<string>();
        public readonly List<string> Breadcrumbs = new List<string>();
        /// <summary>The files of this process (both slots).</summary>
        public readonly List<string> Files = new List<string>();

        /// <summary>Parses a journal file; null if it is not one or its checksum does not match.</summary>
        internal static JournalRecord Parse(byte[] data)
        {
            if (data == null || data.Length < CrashJournal.Magic.Length + 1)
                return null;
            // The checksum line: the first "\nend=" (values have no raw newline).
            int end = -1;
            for (int i = 0; i + 5 < data.Length; i++)
            {
                if (data[i] == '\n' && data[i + 1] == 'e' && data[i + 2] == 'n' && data[i + 3] == 'd' && data[i + 4] == '=')
                {
                    end = i + 1;
                    break;
                }
            }
            if (end < 0 || end + 4 + 16 > data.Length)
                return null;
            ulong want = 0;
            for (int i = 0; i < 16; i++)
            {
                int c = data[end + 4 + i];
                int v = c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : -1;
                if (v < 0)
                    return null;
                want = (want << 4) | (uint)v;
            }
            if (CrashJournal.Fnv(data, 0, end) != want)
                return null;
            string text = new UTF8Encoding(false, false).GetString(data, 0, end);
            var lines = text.Split('\n');
            if (lines.Length == 0 || lines[0] != CrashJournal.Magic)
                return null;
            var r = new JournalRecord();
            for (int i = 1; i < lines.Length; i++)
            {
                string line = lines[i];
                int eq = line.IndexOf('=');
                if (eq <= 0)
                    continue;
                string key = line.Substring(0, eq);
                string value = Unescape(line, eq + 1);
                switch (key)
                {
                    case "pid": r.Pid = (int)ParseLong(value); break;
                    case "start": r.StartMs = ParseLong(value); break;
                    case "written": r.WrittenMs = ParseLong(value); break;
                    case "seq": r.Seq = ParseLong(value); break;
                    case "session": r.SessionId = value; break;
                    case "session_start": r.SessionStartMs = ParseLong(value); break;
                    case "device": r.DeviceId = value; break;
                    case "app_version": r.AppVersion = value; break;
                    case "build_type": r.BuildType = value; break;
                    case "platform": r.Platform = value; break;
                    case "device_model": r.DeviceModel = value; break;
                    case "os_version": r.OsVersion = value; break;
                    case "gpu": r.Gpu = value; break;
                    case "ram_class": r.RamClass = value; break;
                    case "net_type": r.NetType = value; break;
                    case "abi": r.Abi = value; break;
                    case "scene": r.Scene = value; break;
                    case "fg": r.Foreground = value == "1"; break;
                    case "fps":
                    case "frame_ms":
                    case "frame_ms_max":
                    case "mem_mb":
                    case "gc_count":
                        r.PerfKeys.Add(key);
                        r.PerfValues.Add(value);
                        break;
                    case "crumb":
                        r.Breadcrumbs.Add(value);
                        break;
                    default:
                        if (key.StartsWith("ctx.", StringComparison.Ordinal) && GameContext.IsValidKey(key.Substring(4)))
                        {
                            r.ContextKeys.Add(key.Substring(4));
                            r.ContextValues.Add(value);
                        }
                        break;
                }
            }
            return r;
        }

        private static long ParseLong(string s)
        {
            long.TryParse(s, System.Globalization.NumberStyles.Integer, System.Globalization.CultureInfo.InvariantCulture, out long v);
            return v;
        }

        private static string Unescape(string line, int from)
        {
            if (line.IndexOf('\\', from) < 0)
                return line.Substring(from);
            var sb = new StringBuilder(line.Length - from);
            for (int i = from; i < line.Length; i++)
            {
                char c = line[i];
                if (c == '\\' && i + 1 < line.Length)
                {
                    char n = line[++i];
                    sb.Append(n == 'n' ? '\n' : n == 'r' ? '\r' : n);
                }
                else
                {
                    sb.Append(c);
                }
            }
            return sb.ToString();
        }
    }
}
