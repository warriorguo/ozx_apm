using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>How a process ended, as Android recorded it (ApplicationExitInfo, API 30+).</summary>
    internal sealed class ExitRecord
    {
        public int Pid;
        public long TimestampMs;
        public int Reason;
        public int Status;
        public int Importance;
        public long PssKb;
        public long RssKb;
        public string Description = "";
        /// <summary>The copied trace (tombstone or ANR trace), null without one.</summary>
        public string TracePath;
    }

    /// <summary>Where exit records come from (Android: AndroidExitInfo; tests: a fake).</summary>
    internal interface IExitInfoSource
    {
        /// <summary>
        /// This app's recent process exits, newest first. For the pids in
        /// wantTrace whose exit has a trace (ANR; native crash on Android 12+),
        /// the trace is copied into traceDir and TracePath set. Never throws.
        /// </summary>
        List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir);
    }

    /// <summary>ApplicationExitInfo.REASON_* values and names.</summary>
    internal static class ExitReasons
    {
        public const int Unknown = 0, ExitSelf = 1, Signaled = 2, LowMemory = 3, Crash = 4, CrashNative = 5, Anr = 6,
            InitializationFailure = 7, PermissionChange = 8, ExcessiveResourceUsage = 9, UserRequested = 10, UserStopped = 11,
            DependencyDied = 12, Other = 13, Freezer = 14, PackageStateChange = 15, PackageUpdated = 16;

        public static string Name(int reason)
        {
            switch (reason)
            {
                case Unknown: return "UNKNOWN";
                case ExitSelf: return "EXIT_SELF";
                case Signaled: return "SIGNALED";
                case LowMemory: return "LOW_MEMORY";
                case Crash: return "CRASH";
                case CrashNative: return "CRASH_NATIVE";
                case Anr: return "ANR";
                case InitializationFailure: return "INITIALIZATION_FAILURE";
                case PermissionChange: return "PERMISSION_CHANGE";
                case ExcessiveResourceUsage: return "EXCESSIVE_RESOURCE_USAGE";
                case UserRequested: return "USER_REQUESTED";
                case UserStopped: return "USER_STOPPED";
                case DependencyDied: return "DEPENDENCY_DIED";
                case Other: return "OTHER";
                case Freezer: return "FREEZER";
                case PackageStateChange: return "PACKAGE_STATE_CHANGE";
                case PackageUpdated: return "PACKAGE_UPDATED";
                default: return "REASON_" + reason.ToString(CultureInfo.InvariantCulture);
            }
        }
    }

    /// <summary>
    /// Reports, on the next launch, how earlier processes ended (APM-7). For
    /// every crash journal of an earlier process: Android's exit record of
    /// that pid (API 30+), with the tombstone or ANR trace when there is one,
    /// decides whether it was a crash and which kind; the journal gives the
    /// session, version, scene, context and breadcrumbs of that moment.
    ///
    /// Reported as a crash:
    /// - native crash (CRASH_NATIVE), Java crash (CRASH), ANR: always;
    /// - LOW_MEMORY ("oom") and other kills by the system or the user
    ///   ("killed": SIGNALED, EXCESSIVE_RESOURCE_USAGE, FREEZER, ...): only
    ///   when the game was in the foreground (not paused per the journal, or
    ///   visible per Android's importance); a process reclaimed in the
    ///   background is normal Android behaviour, not a crash;
    /// - no exit record (Android before 11, or none kept) and the journal says
    ///   foreground: "unknown" (a crash, ANR or kill Android did not explain).
    /// Never reported: EXIT_SELF (the app quit), permission/package changes, a
    /// clean shutdown (its journal was deleted).
    ///
    /// Unity's own tombstone: Unity's crash handler writes the crashed
    /// thread's backtrace to persistentDataPath/tombstone_NN before it forwards
    /// the signal. On Android 16 (A33) ActivityManager, told of the native
    /// crash by debuggerd, sometimes kills the process before the signal ends
    /// it: the exit is then REASON_CRASH with no trace, like a Java crash (2 of
    /// 5 runs). When an exit has no trace (CRASH; CRASH_NATIVE without a
    /// tombstone, Android 11; SIGNALED, UNKNOWN, OTHER; no exit record), a
    /// Unity tombstone of the same pid written during that process (its
    /// Timestamp from the process start to a few seconds after the exit) makes
    /// it a native crash with that stack. Unity's files are only read (a
    /// bounded prefix: header and backtrace), never changed or deleted.
    ///
    /// Runs on a background thread at startup; the crash events keep the
    /// crashed session's fields, get an event id derived from the process (a
    /// re-report after a failed write is a duplicate the server drops), and the
    /// journal is deleted once they are on disk.
    /// </summary>
    internal sealed class CrashReporter
    {
        internal const int MaxJournalsProcessed = 5;
        internal const int MaxTraceBytes = 4 * 1024 * 1024;
        internal const int MaxAnrChars = 8 * 1024 * 1024;
        internal const int PersistTimeoutMs = 3000;
        internal const string TracePrefix = "trace-";
        /// <summary>Bytes read from a Unity tombstone (header, registers, backtrace; the memory maps after it are not read).</summary>
        internal const int MaxUnityTombstoneBytes = 64 * 1024;
        /// <summary>Unity tombstone files looked at (Unity rotates three).</summary>
        internal const int MaxUnityTombstoneFiles = 8;
        /// <summary>A Unity tombstone may be this much later than the exit record (the kill follows the crash).</summary>
        internal const long UnityTombstoneAfterExitMs = 5000;
        /// <summary>... and at most this much earlier (the crash precedes the kill by well under a second).</summary>
        internal const long UnityTombstoneBeforeExitMs = 60000;
        /// <summary>Without an exit record: at most this long after the journal's last write (it is rewritten every 5 s).</summary>
        internal const long UnityTombstoneAfterJournalMs = 60000;

        private readonly string _dir;
        private readonly IExitInfoSource _source;
        private readonly Action<CrashEvent> _enqueue;
        private readonly Func<bool> _allowed;
        private readonly Func<int, bool> _waitPersisted;

        /// <summary>
        /// The current device id: a journal of another id (written before
        /// ResetDeviceId) is deleted, never reported. Null: no check (tests).
        /// </summary>
        internal Func<string> CurrentDeviceId;

        /// <summary>
        /// Where Unity's crash handler writes its tombstones
        /// (Application.persistentDataPath); null: not looked at.
        /// </summary>
        internal string UnityTombstoneDir;

        /// <summary>Crash events enqueued, journals without a crash, journals kept for a retry.</summary>
        internal int Reported, NotCrashes, Kept;
        /// <summary>The last run's events (tests).</summary>
        internal readonly List<CrashEvent> LastEvents = new List<CrashEvent>();

        internal CrashReporter(string dir, IExitInfoSource source, Action<CrashEvent> enqueue, Func<bool> allowed, Func<int, bool> waitPersisted)
        {
            _dir = dir;
            _source = source;
            _enqueue = enqueue;
            _allowed = allowed;
            _waitPersisted = waitPersisted;
        }

        /// <summary>
        /// Reports the journals of earlier processes in the directory (not the
        /// one of ownPid/ownStartMs). Synchronous; never throws.
        /// </summary>
        internal void ProcessPrevious(int ownPid, long ownStartMs)
        {
            try
            {
                Process(ownPid, ownStartMs);
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashReporter", ex);
            }
        }

        private void Process(int ownPid, long ownStartMs)
        {
            LastEvents.Clear();
            DeleteStaleTraces();
            var invalid = new List<string>();
            var journals = CrashJournal.ReadPrevious(_dir, ownPid, ownStartMs, invalid);
            foreach (var path in invalid)
                TryDelete(path);
            if (journals.Count == 0)
                return;
            // Newest first; a crash loop leaves many: report the newest few.
            for (int i = journals.Count - 1; i >= MaxJournalsProcessed; i--)
            {
                foreach (var f in journals[i].Files)
                    TryDelete(f);
                journals.RemoveAt(i);
            }

            if (!_allowed())
                return; // Undecided keeps them; Denied deletes the directory
            var pids = new List<int>();
            foreach (var j in journals)
                pids.Add(j.Pid);
            List<ExitRecord> exits = null;
            if (_source != null)
            {
                try
                {
                    exits = _source.Query(pids, _dir);
                }
                catch (Exception ex)
                {
                    ApmClient.ReportInternalError("CrashReporter.ExitInfo", ex);
                }
            }
            exits = exits ?? new List<ExitRecord>();

            var reportedJournals = new List<JournalRecord>();
            try
            {
                Report(journals, exits, reportedJournals);
            }
            finally
            {
                // Copied traces never stay (also when consent was withdrawn meanwhile).
                foreach (var e in exits)
                {
                    if (e.TracePath != null)
                        TryDelete(e.TracePath);
                }
            }
            if (reportedJournals.Count == 0)
                return;
            // Delete the journals once the events are on disk; if that takes
            // too long they are reported again next time, with the same event ids.
            bool persisted = _waitPersisted == null || _waitPersisted(PersistTimeoutMs);
            foreach (var j in reportedJournals)
            {
                if (!persisted)
                {
                    Kept++;
                    continue;
                }
                foreach (var f in j.Files)
                    TryDelete(f);
            }
        }

        private void Report(List<JournalRecord> journals, List<ExitRecord> exits, List<JournalRecord> reportedJournals)
        {
            string device = CurrentDeviceId?.Invoke();
            foreach (var j in journals)
            {
                if (device != null && j.DeviceId != device)
                {
                    // Written before ResetDeviceId: the old id must not be sent.
                    NotCrashes++;
                    foreach (var f in j.Files)
                        TryDelete(f);
                    continue;
                }
                var exit = Match(exits, j);
                CrashTraceParser.Result trace = null;
                if (exit != null && exit.TracePath != null)
                {
                    trace = ReadTrace(exit);
                    TryDelete(exit.TracePath);
                }
                if (trace == null && MayBeNativeWithoutTrace(exit))
                    trace = FindUnityTombstone(j, exit);
                var evt = Build(j, exit, trace);
                if (evt == null)
                {
                    NotCrashes++;
                    foreach (var f in j.Files)
                        TryDelete(f);
                    continue;
                }
                if (!_allowed())
                    return; // consent withdrawn meanwhile: keep the journals (Undecided keeps, Denied deletes them)
                if (device != null && CurrentDeviceId() != device)
                    return; // ResetDeviceId meanwhile: its own deletion takes the journals
                _enqueue(evt);
                LastEvents.Add(evt);
                Reported++;
                reportedJournals.Add(j);
            }
        }

        // The newest exit record of the journal's pid that is not older than the process.
        private static ExitRecord Match(List<ExitRecord> exits, JournalRecord j)
        {
            ExitRecord best = null;
            foreach (var e in exits)
            {
                if (e.Pid != j.Pid || e.TimestampMs + 1000 < j.StartMs)
                    continue;
                if (best == null || e.TimestampMs > best.TimestampMs)
                    best = e;
            }
            return best;
        }

        // Exits after which a Unity tombstone of the process is looked for.
        private static bool MayBeNativeWithoutTrace(ExitRecord e)
        {
            if (e == null)
                return true;
            switch (e.Reason)
            {
                case ExitReasons.Crash:
                case ExitReasons.CrashNative:
                case ExitReasons.Signaled:
                case ExitReasons.Unknown:
                case ExitReasons.Other:
                    return true;
                default:
                    return false;
            }
        }

        /// <summary>
        /// Unity's tombstone of the journal's process: same pid, written from
        /// the process start up to shortly after the exit (or the journal's
        /// last write without an exit record). The newest such; null without.
        /// </summary>
        internal CrashTraceParser.Result FindUnityTombstone(JournalRecord j, ExitRecord exit)
        {
            if (string.IsNullOrEmpty(UnityTombstoneDir))
                return null;
            long from = j.StartMs - 1000; // a tombstone before the process started is another process's (pid reuse)
            long to;
            if (exit != null && exit.TimestampMs > 0)
            {
                from = Math.Max(from, exit.TimestampMs - UnityTombstoneBeforeExitMs);
                to = exit.TimestampMs + UnityTombstoneAfterExitMs;
            }
            else
            {
                to = Math.Max(j.WrittenMs, j.StartMs) + UnityTombstoneAfterJournalMs;
            }
            try
            {
                if (!Directory.Exists(UnityTombstoneDir))
                    return null;
                CrashTraceParser.Result best = null;
                int looked = 0;
                foreach (var path in Directory.GetFiles(UnityTombstoneDir, "tombstone_*"))
                {
                    if (!IsUnityTombstoneName(Path.GetFileName(path)))
                        continue;
                    if (++looked > MaxUnityTombstoneFiles)
                        break;
                    long written = new DateTimeOffset(File.GetLastWriteTimeUtc(path)).ToUnixTimeMilliseconds();
                    if (written < from)
                        continue; // older than the process: not read
                    var r = ReadUnityTombstone(path);
                    if (r == null || r.Pid != j.Pid)
                        continue;
                    long at = r.TimestampMs > 0 ? r.TimestampMs : written;
                    if (at < from || at > to)
                        continue;
                    if (r.TimestampMs <= 0)
                        r.TimestampMs = written;
                    if (best == null || r.TimestampMs > best.TimestampMs)
                        best = r;
                }
                return best;
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashReporter.UnityTombstone", ex);
                return null;
            }
        }

        // "tombstone_" and 1 to 4 digits (Unity: tombstone_00 .. tombstone_02).
        internal static bool IsUnityTombstoneName(string name)
        {
            const string prefix = "tombstone_";
            if (name == null || !name.StartsWith(prefix, StringComparison.Ordinal))
                return false;
            int n = name.Length - prefix.Length;
            if (n < 1 || n > 4)
                return false;
            for (int i = prefix.Length; i < name.Length; i++)
            {
                if (name[i] < '0' || name[i] > '9')
                    return false;
            }
            return true;
        }

        private static CrashTraceParser.Result ReadUnityTombstone(string path)
        {
            byte[] data;
            int read = 0;
            using (var fs = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite))
            {
                data = new byte[(int)Math.Min(fs.Length, MaxUnityTombstoneBytes)];
                while (read < data.Length)
                {
                    int r = fs.Read(data, read, data.Length - read);
                    if (r <= 0)
                        break;
                    read += r;
                }
            }
            return CrashTraceParser.ParseUnityTombstone(data, read);
        }

        private static CrashTraceParser.Result ReadTrace(ExitRecord exit)
        {
            try
            {
                if (exit.Reason == ExitReasons.Anr)
                {
                    using (var reader = new StreamReader(exit.TracePath, new UTF8Encoding(false, false), false, 64 * 1024))
                        return CrashTraceParser.ParseAnr(reader, MaxAnrChars);
                }
                byte[] data;
                using (var fs = new FileStream(exit.TracePath, FileMode.Open, FileAccess.Read, FileShare.ReadWrite))
                {
                    int n = (int)Math.Min(fs.Length, MaxTraceBytes);
                    data = new byte[n];
                    int read = 0;
                    while (read < n)
                    {
                        int r = fs.Read(data, read, n - read);
                        if (r <= 0)
                            break;
                        read += r;
                    }
                    if (read < n)
                        Array.Resize(ref data, read);
                }
                return CrashTraceParser.ParseTombstone(data, data.Length);
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashReporter.ReadTrace", ex);
                return null;
            }
        }

        /// <summary>
        /// The crash type of a journal and its exit record, or null when it was
        /// not a crash (see the class comment).
        /// </summary>
        internal static string Classify(JournalRecord j, ExitRecord e, out bool foreground)
        {
            foreground = j.Foreground || (e != null && e.Importance > 0 && e.Importance <= 200);
            if (e == null)
                return j.Foreground ? "unknown" : null;
            switch (e.Reason)
            {
                case ExitReasons.CrashNative: return "native";
                case ExitReasons.Crash: return "java";
                case ExitReasons.Anr: return "anr";
                case ExitReasons.LowMemory: return foreground ? "oom" : null;
                case ExitReasons.ExitSelf:
                case ExitReasons.PermissionChange:
                case ExitReasons.PackageStateChange:
                case ExitReasons.PackageUpdated:
                    return null;
                default:
                    // SIGNALED, EXCESSIVE_RESOURCE_USAGE, INITIALIZATION_FAILURE,
                    // USER_REQUESTED/STOPPED, DEPENDENCY_DIED, FREEZER, OTHER, UNKNOWN.
                    return foreground ? "killed" : null;
            }
        }

        internal static CrashEvent Build(JournalRecord j, ExitRecord e, CrashTraceParser.Result trace)
        {
            string type = Classify(j, e, out bool foreground);
            bool unity = trace != null && trace.Format == CrashTraceParser.UnityFormat;
            if (unity)
                type = "native"; // Unity's handler wrote the backtrace of a fatal signal of this process
            if (type == null)
                return null;
            var evt = new CrashEvent
            {
                crash_type = type,
                timestamp = e != null && e.TimestampMs > 0 ? e.TimestampMs : j.WrittenMs,
                event_id = EventId(j),
                app_version = j.AppVersion,
                platform = j.Platform,
                device_model = j.DeviceModel,
                os_version = j.OsVersion,
                session_id = j.SessionId,
                device_id = j.DeviceId,
                scene = j.Scene,
                build_type = j.BuildType,
                gpu = j.Gpu,
                ram_class = j.RamClass,
                net_type = j.NetType,
                abi = j.Abi,
                foreground = foreground,
                stack = trace != null ? trace.Stack : "",
            };
            evt.breadcrumbs.AddRange(j.Breadcrumbs);
            evt.ctx = Context(j);
            if (e != null)
            {
                evt.exit_reason = ExitReasons.Name(e.Reason);
                evt.exit_status = e.Status;
                evt.exit_description = CrashTraceParser.Cut(e.Description ?? "", 1000);
                evt.importance = e.Importance;
                evt.pss_kb = e.PssKb;
                evt.rss_kb = e.RssKb;
            }
            if (trace != null)
            {
                evt.signal = trace.Signal;
                evt.signal_code = trace.SignalCode;
                evt.fault_addr = trace.FaultAddr;
                evt.abort_message = trace.AbortMessage;
            }
            if (string.IsNullOrEmpty(evt.signal) && e != null && e.Reason == ExitReasons.CrashNative && e.Status > 0)
                evt.signal = CrashTraceParser.SignalName(e.Status); // Android 11: no tombstone, the status is the signal
            if (j.SessionStartMs > 0 && evt.timestamp > j.SessionStartMs)
                evt.session_ms = evt.timestamp - j.SessionStartMs;

            var details = new List<string>
            {
                "detected_by", (e != null ? "exit_info" : "journal") + (unity ? "+unity_tombstone" : ""),
                "journal_age_ms", Math.Max(0, evt.timestamp - j.WrittenMs).ToString(CultureInfo.InvariantCulture),
                "uptime_ms", Math.Max(0, evt.timestamp - j.StartMs).ToString(CultureInfo.InvariantCulture),
                "trace", trace != null ? trace.Format : (e != null && (e.Reason == ExitReasons.CrashNative || e.Reason == ExitReasons.Anr) ? "none" : "n/a"),
            };
            if (trace != null && trace.Cause.Length > 0)
            {
                details.Add("cause");
                details.Add(trace.Cause);
            }
            for (int i = 0; i < j.PerfKeys.Count; i++)
            {
                details.Add(j.PerfKeys[i]);
                details.Add(j.PerfValues[i]);
            }
            evt.details = details.ToArray();
            evt.fingerprint = Fingerprint(evt, trace);
            return evt;
        }

        private static ContextSnapshot Context(JournalRecord j)
        {
            if (j.ContextKeys.Count == 0)
                return ContextSnapshot.Empty;
            var pairs = new SortedDictionary<string, string>(StringComparer.Ordinal);
            for (int i = 0; i < j.ContextKeys.Count && pairs.Count < GameContext.MaxKeys; i++)
                pairs[j.ContextKeys[i]] = CrashTraceParser.Cut(j.ContextValues[i], GameContext.MaxValueLength);
            var keys = new string[pairs.Count];
            var values = new string[pairs.Count];
            int k = 0;
            foreach (var kv in pairs)
            {
                keys[k] = kv.Key;
                values[k++] = kv.Value;
            }
            return new ContextSnapshot(0, keys, values);
        }

        /// <summary>32 hex digits from the device, pid and process start: the same process always gets the same id.</summary>
        internal static string EventId(JournalRecord j)
        {
            string seed = "crash|" + j.DeviceId + "|" + j.Pid.ToString(CultureInfo.InvariantCulture) + "|" + j.StartMs.ToString(CultureInfo.InvariantCulture);
            ulong a = Fnv(seed, 14695981039346656037UL), b = Fnv(seed, 0x9E3779B97F4A7C15UL);
            return a.ToString("x16", CultureInfo.InvariantCulture) + b.ToString("x16", CultureInfo.InvariantCulture);
        }

        // The SDK's own fingerprint (the server computes the one it groups by).
        private static string Fingerprint(CrashEvent evt, CrashTraceParser.Result trace)
        {
            var sb = new StringBuilder();
            sb.Append(evt.crash_type).Append('|').Append(evt.exit_reason).Append('|').Append(evt.signal);
            if (trace != null && trace.Threads.Count > 0)
            {
                var frames = trace.Threads[0].Frames;
                for (int i = 0; i < frames.Count && i < 3; i++)
                    sb.Append('|').Append(frames[i].Java ?? frames[i].Module + "+" + frames[i].Pc.ToString("x", CultureInfo.InvariantCulture));
            }
            return Fnv(sb.ToString(), 14695981039346656037UL).ToString("x16", CultureInfo.InvariantCulture);
        }

        private static ulong Fnv(string s, ulong h)
        {
            var bytes = Encoding.UTF8.GetBytes(s);
            foreach (var b in bytes)
            {
                h ^= b;
                h *= 1099511628211UL;
            }
            return h;
        }

        private void DeleteStaleTraces()
        {
            try
            {
                if (!Directory.Exists(_dir))
                    return;
                foreach (var f in Directory.GetFiles(_dir, TracePrefix + "*"))
                    TryDelete(f);
            }
            catch
            {
                // Best effort.
            }
        }

        private static void TryDelete(string path)
        {
            try
            {
                File.Delete(path);
            }
            catch
            {
                // Deleted already or not deletable: harmless.
            }
        }
    }
}
