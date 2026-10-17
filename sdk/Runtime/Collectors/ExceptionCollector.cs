using System;
using System.Collections.Generic;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Receives every log line the SDK's log callback sees (APM-6: the recent
    /// warnings/errors ring). Called on the logging thread, before the error
    /// path, for every log type; must not throw, allocate per call or log.
    /// </summary>
    internal interface ILogSink
    {
        void OnLog(string message, string stackTrace, LogType type, bool mainThread);
    }

    /// <summary>
    /// Captures errors, exceptions and asserts logged on any thread, and
    /// exceptions no code caught (APM-3).
    ///
    /// Sources: Application.logMessageReceivedThreaded (the one log callback,
    /// called on the logging thread, main or not; Unity's own output is not
    /// changed), AppDomain.UnhandledException and
    /// TaskScheduler.UnobservedTaskException (reported with "unhandled").
    ///
    /// De-duplication, per window of WindowMs (also ended by FlushPending: app
    /// paused, Flush, Stop):
    /// - The first occurrence of a fingerprint is enqueued at once (persisted
    ///   within milliseconds) with count 1, its breadcrumbs, the game context,
    ///   the per-session error sequence number and the time since session
    ///   start, for at most MaxImmediatePerWindow fingerprints per window.
    /// - Every further occurrence (and a first one beyond that budget) is
    ///   counted in a bucket of its fingerprint keyed by the game context it
    ///   happened in (and whether it was unhandled). At the end of the window
    ///   each bucket is one event with its count, the context, and the time,
    ///   sequence number and session time of its first occurrence. So
    ///   sum(count) is exact and the distribution over context values is
    ///   recoverable on the server.
    /// - At most MaxBucketsPerFingerprint buckets per fingerprint and
    ///   MaxBucketsPerWindow in all; further contexts go to the fingerprint's
    ///   overflow bucket, whose event has ctx_merged and the context entries
    ///   all its occurrences shared.
    /// - At most MaxFingerprintsPerWindow fingerprints per window. Errors with
    ///   other fingerprints are counted in one summary event per window
    ///   ("[OzxApm] N errors of M kinds not reported one by one"), and tracked
    ///   normally in the next window if they occur again.
    /// Every count reaches the server; per window the collector enqueues at most
    /// MaxImmediatePerWindow + MaxBucketsPerWindow + MaxFingerprintsPerWindow + 1
    /// events.
    ///
    /// Cost: a repeat (the same text, up to the numbers in its first line,
    /// within the window) is two hashes, one lookup and a counter under a
    /// lock: no parsing, no allocation. A new text is parsed once (outside the
    /// lock). Thread-safe: logs arrive on any thread, the window is closed by
    /// the main thread's Update. Nothing is ever logged or enqueued while the
    /// lock is held (Unity may hold its own log lock while calling us).
    /// </summary>
    public class ExceptionCollector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;
        private SessionClock _clock;

        private volatile bool _isActive;
        private int _mainThreadId = -1;

        // ---- Limits (RELEASE.md, APM-3) ----
        internal const int WindowMs = 60000;
        internal const int MaxImmediatePerWindow = 30;
        internal const int MaxFingerprintsPerWindow = 100;
        internal const int MaxBucketsPerFingerprint = 8;
        internal const int MaxBucketsPerWindow = 64;
        // Distinct texts remembered per window for the cheap repeat path; a text
        // beyond it is parsed every time (correct, only slower).
        internal const int MaxRawKeys = 512;
        private const int MaxUntrackedFingerprints = 1024;

        // What is sent (APM-4). The embedded exception is extracted from the
        // full message before the message is cut, and kept under the stack's
        // limit, not the message's.
        internal const int MaxMessageLength = 500;
        internal const int MaxStackLength = 8000;
        internal const int MaxEmbeddedLength = 8000;
        // At most this much of a message is parsed for an embedded exception.
        private const int MaxEmbeddedSourceLength = 64 * 1024;

        // Unhandled exceptions (see HandleUnhandled).
        internal const string UnhandledDomain = "domain";
        internal const string UnhandledDomainTerminating = "domain_terminating";
        internal const string UnhandledTask = "task";
        // A logged exception and the unhandled-exception report of the same
        // exception on the same thread within this time are one occurrence.
        private const int EchoWindowMs = 2000;
        // How long a first occurrence logged as an exception off the main thread
        // waits before it is enqueued, so that the unhandled-exception report
        // that may follow can still mark it.
        private const int HoldMs = 100;

        private readonly object _sync = new object();

        // ---- Window state, guarded by _sync ----
        private readonly Dictionary<string, Entry> _entries =
            new Dictionary<string, Entry>(MaxFingerprintsPerWindow, StringComparer.Ordinal);
        // Entry of each distinct log text seen in the window (Untracked for texts
        // of fingerprints beyond MaxFingerprintsPerWindow), so that a repeat is
        // counted without parsing it again or allocating. Keyed by hashes of the
        // text.
        private readonly Dictionary<RawKey, Entry> _byRawKey =
            new Dictionary<RawKey, Entry>(MaxRawKeys, RawKeyComparer.Instance);
        private static readonly Entry Untracked = new Entry();
        private int _windowStartTick;
        private int _immediateSent;
        private int _bucketsUsed;
        private int _untrackedCount;
        private Occurrence _untrackedFirst;
        private readonly HashSet<string> _untrackedFingerprints = new HashSet<string>(StringComparer.Ordinal);
        // First occurrences held for HoldMs (off-main-thread logged exceptions).
        private readonly List<HeldEvent> _held = new List<HeldEvent>();
        private int _heldCount; // _held.Count, readable without the lock
        private readonly List<ExceptionEvent> _released = new List<ExceptionEvent>(); // main thread (Update) only
        // Per-session error sequence.
        private int _errorSeq;
        private int _seqGeneration = int.MinValue;
        // Totals since start (ApmStats).
        private long _occurrences;
        private long _eventsEnqueued;
        private long _untrackedTotal;

        // Echo detection between a logged exception and its unhandled report,
        // per thread (only off the main thread).
        [ThreadStatic] private static ExceptionCollector t_logOwner;
        [ThreadStatic] private static string t_logFingerprint;
        [ThreadStatic] private static int t_logTick;
        [ThreadStatic] private static object t_logTarget; // HeldEvent's event or the Bucket counted in
        [ThreadStatic] private static Occurrence t_logOccurrence;
        [ThreadStatic] private static int t_logWindow;
        [ThreadStatic] private static ExceptionCollector t_echoOwner;
        [ThreadStatic] private static string t_echoFingerprint;
        [ThreadStatic] private static int t_echoTick;
        private int _windowId;

        // Breadcrumbs for crash context: a ring of (time, message), formatted
        // only when they are sent, so recording one does not allocate. Guarded by _sync.
        private const int MaxBreadcrumbs = 50;
        private readonly long[] _breadcrumbTimes = new long[MaxBreadcrumbs];
        private readonly string[] _breadcrumbMessages = new string[MaxBreadcrumbs];
        private int _breadcrumbStart;
        private int _breadcrumbCount;
        // Sent with the first occurrence of an exception: the most recent ones,
        // each cut to a bounded length (at most ~4.5 KB per event).
        internal const int MaxBreadcrumbsPerEvent = 20;
        internal const int MaxBreadcrumbLength = 200;

        // Delegates created once, so that Start/Stop add and remove the same instances.
        private readonly Application.LogCallback _onLog;
        private readonly UnhandledExceptionEventHandler _onUnhandled;
        private readonly EventHandler<UnobservedTaskExceptionEventArgs> _onUnobserved;

        /// <summary>One fingerprint in the current window: the text that is sent, and its buckets.</summary>
        private sealed class Entry
        {
            public string Fingerprint;
            public string LogType;
            public string Message;
            public string Stack;
            public string Embedded;
            public Bucket[] Buckets;
            public int BucketCount;
            public Bucket Last;     // the bucket counted last: checked first
            public Bucket Overflow;
        }

        /// <summary>Occurrences of one fingerprint in one game context (and kind).</summary>
        private sealed class Bucket
        {
            public ContextSnapshot Ctx;
            public string Unhandled;
            public int Count;
            public long FirstMs;
            public int FirstSeq;
            public long FirstSessionMs;
            public bool Merged;
        }

        private struct Occurrence
        {
            public long TimeMs;
            public int Seq;
            public long SessionMs;
            public ContextSnapshot Ctx;
            public string Unhandled;
        }

        private struct HeldEvent
        {
            public ExceptionEvent Event;
            public int Tick;
        }

        // Key of a log text for the repeat path. The message's first line is
        // hashed with every run of digits counted as one '#': that is where
        // ids and counts that change from call to call are (an error logged
        // every frame with the frame's roomId or entity id stays one cheap
        // repeat), and the fingerprint never depends on their values (the
        // message template replaces numbers). The rest of the message (an
        // embedded exception's frames, whose outer frames decide the group and
        // can be anywhere in a long text) and the stack are hashed exactly.
        // Two texts share an entry only if all hashes and lengths are equal;
        // the only texts it merges with different fingerprints are exception
        // type names that differ only in their digits (e.g. Error1Exception
        // and Error2Exception in the same place).
        private struct RawKey
        {
            public ulong MessageHash;
            public int RestLength;
            public int StackHash;
            public int StackLength;
            public LogType Type;
        }

        // A class comparer: no boxing, and no reliance on EqualityComparer<T>.Default
        // for a value type under IL2CPP.
        private sealed class RawKeyComparer : IEqualityComparer<RawKey>
        {
            public static readonly RawKeyComparer Instance = new RawKeyComparer();

            public bool Equals(RawKey a, RawKey b)
            {
                return a.MessageHash == b.MessageHash && a.StackHash == b.StackHash &&
                       a.RestLength == b.RestLength && a.StackLength == b.StackLength && a.Type == b.Type;
            }

            public int GetHashCode(RawKey k)
            {
                return unchecked((int)k.MessageHash ^ (int)(k.MessageHash >> 32) ^ (k.StackHash * 31) ^ k.RestLength ^ (int)k.Type);
            }
        }

        // Hash of s in which, in the first line, a run of digits counts as one
        // '#'. Two 32-bit lanes (djb2-xor, alternate characters) make a 64-bit
        // key at the speed of string.GetHashCode. No allocation. At most the
        // first MaxKeyChars characters are read (a pathological megabyte log
        // stays bounded); restLength still covers the whole text.
        private const int MaxKeyChars = 64 * 1024;

        private static ulong MessageKeyHash(string s, out int restLength)
        {
            uint h1 = 5381, h2 = 52711;
            int i = 0, total = s.Length, n = Math.Min(total, MaxKeyChars);
            bool inDigits = false, odd = false;
            for (; i < n && s[i] != '\n'; i++)
            {
                char c = s[i];
                if (c >= '0' && c <= '9')
                {
                    if (inDigits)
                        continue;
                    inDigits = true;
                    c = '#';
                }
                else
                {
                    inDigits = false;
                }
                if (odd)
                    h2 = unchecked(((h2 << 5) + h2) ^ c);
                else
                    h1 = unchecked(((h1 << 5) + h1) ^ c);
                odd = !odd;
            }
            restLength = total - i;
            for (; i + 1 < n; i += 2)
            {
                h1 = unchecked(((h1 << 5) + h1) ^ s[i]);
                h2 = unchecked(((h2 << 5) + h2) ^ s[i + 1]);
            }
            if (i < n)
                h1 = unchecked(((h1 << 5) + h1) ^ s[i]);
            return ((ulong)h1 << 32) | h2;
        }

        public bool IsActive => _isActive;

        /// <summary>APM-6: receives every log line (see ILogSink). Set before Start.</summary>
        internal ILogSink LogSink;

        /// <summary>
        /// Called (on the dying thread) after an unhandled exception that ends the
        /// process was recorded and the window flushed: writes the queue to disk.
        /// </summary>
        internal Action PersistNow;

        // Test seams: receive the events instead of ApmClient.EnqueueEvent, and
        // activate without subscribing to Unity's log callback.
        internal Action<ExceptionEvent> EnqueueForTests;
        internal void ActivateForTests()
        {
            _mainThreadId = Environment.CurrentManagedThreadId;
            Volatile.Write(ref _windowStartTick, Environment.TickCount);
            _isActive = true;
        }
        internal SessionClock ClockForTests { set => _clock = value; }
        /// <summary>Ends the window as if WindowMs had passed (main thread).</summary>
        internal void ExpireWindowForTests() => Volatile.Write(ref _windowStartTick, Environment.TickCount - WindowMs - 1);
        internal void ReleaseHeldForTests() => ReleaseHeld(Environment.TickCount + HoldMs + 1, true);
        internal void OnUnhandledForTests(Exception ex, bool terminating) =>
            HandleUnhandled(ex, terminating ? UnhandledDomainTerminating : UnhandledDomain, terminating);
        internal void OnUnobservedForTests(AggregateException ex) => HandleUnhandled(Unwrap(ex), UnhandledTask, false);

        public ExceptionCollector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
            _clock = client != null && client.Session != null ? client.Session.Clock : null;
            _onLog = OnLogMessage;
            _onUnhandled = OnUnhandledException;
            _onUnobserved = OnUnobservedTaskException;
        }

        public void Start()
        {
            // Main thread (ApmClient).
            _mainThreadId = Environment.CurrentManagedThreadId;
            Volatile.Write(ref _windowStartTick, Environment.TickCount);
            _isActive = true;

            // One callback for every thread: logMessageReceivedThreaded is called
            // on the logging thread, the main thread included (subscribing to
            // logMessageReceived as well would report main-thread logs twice).
            Application.logMessageReceivedThreaded += _onLog;
            AppDomain.CurrentDomain.UnhandledException += _onUnhandled;
            TaskScheduler.UnobservedTaskException += _onUnobserved;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            int now = Environment.TickCount;
            if (unchecked(now - Volatile.Read(ref _windowStartTick)) >= WindowMs)
                FlushPending();
            else if (Volatile.Read(ref _heldCount) > 0)
                ReleaseHeld(now, false);
        }

        public void Stop()
        {
            _isActive = false;
            Application.logMessageReceivedThreaded -= _onLog;
            AppDomain.CurrentDomain.UnhandledException -= _onUnhandled;
            TaskScheduler.UnobservedTaskException -= _onUnobserved;

            // Final flush
            FlushPending();
        }

        /// <summary>
        /// Ends the window: enqueues the counts collected so far (e.g. before
        /// going to background). Any thread.
        /// </summary>
        public void FlushPending()
        {
            List<ExceptionEvent> events = null;
            lock (_sync)
            {
                events = CloseWindowLocked();
            }
            EnqueueAll(events);
        }

        /// <summary>Totals since start: occurrences seen, events enqueued, occurrences only in a summary.</summary>
        internal void GetStats(out long occurrences, out long eventsEnqueued, out long untracked)
        {
            lock (_sync)
            {
                occurrences = _occurrences;
                eventsEnqueued = _eventsEnqueued;
                untracked = _untrackedTotal;
            }
        }

        /// <summary>
        /// Records a breadcrumb for crash context. Does not allocate. Any thread.
        /// </summary>
        public void RecordBreadcrumb(string message)
        {
            if (string.IsNullOrEmpty(message))
                return;
            long now = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            lock (_sync)
            {
                RecordBreadcrumbLocked(message, now);
            }
        }

        private void RecordBreadcrumbLocked(string message, long nowMs)
        {
            int i = (_breadcrumbStart + _breadcrumbCount) % MaxBreadcrumbs;
            if (_breadcrumbCount == MaxBreadcrumbs)
                _breadcrumbStart = (_breadcrumbStart + 1) % MaxBreadcrumbs;
            else
                _breadcrumbCount++;
            _breadcrumbTimes[i] = nowMs;
            _breadcrumbMessages[i] = message;
        }

        /// <summary>
        /// Gets current breadcrumbs for crash reports, oldest first, as
        /// "[HH:mm:ss.fff] message" (UTC).
        /// </summary>
        public List<string> GetBreadcrumbs()
        {
            lock (_sync)
            {
                return SnapshotBreadcrumbsLocked(MaxBreadcrumbs, int.MaxValue);
            }
        }

        /// <summary>The most recent breadcrumbs (at most max), oldest first, each cut to maxLength.</summary>
        internal List<string> SnapshotBreadcrumbs(int max, int maxLength)
        {
            lock (_sync)
            {
                return SnapshotBreadcrumbsLocked(max, maxLength);
            }
        }

        private List<string> SnapshotBreadcrumbsLocked(int max, int maxLength)
        {
            int n = Math.Min(max, _breadcrumbCount);
            var list = new List<string>(n);
            for (int k = _breadcrumbCount - n; k < _breadcrumbCount; k++)
            {
                int i = (_breadcrumbStart + k) % MaxBreadcrumbs;
                string time = DateTimeOffset.FromUnixTimeMilliseconds(_breadcrumbTimes[i]).UtcDateTime
                    .ToString("HH:mm:ss.fff", CultureInfo.InvariantCulture);
                string text = "[" + time + "] " + _breadcrumbMessages[i];
                list.Add(text.Length > maxLength ? Truncate(text, maxLength) : text);
            }
            return list;
        }

        private void OnLogMessage(string message, string stackTrace, LogType type)
        {
            // An exception thrown from a log callback would be logged again and
            // land right back here: never let anything escape.
            try
            {
                HandleLogMessage(message, stackTrace, type);
            }
            catch
            {
                // Swallowed on purpose.
            }
        }

        // Internal for tests: the log callback's work. Any thread.
        internal void HandleLogMessage(string message, string stackTrace, LogType type)
        {
            if (!_isActive)
                return;

            bool mainThread = Environment.CurrentManagedThreadId == _mainThreadId;
            var sink = LogSink;
            if (sink != null)
            {
                try
                {
                    sink.OnLog(message, stackTrace, type, mainThread);
                }
                catch
                {
                    // A sink must not break exception capture.
                }
            }

            // Only capture errors and exceptions
            if (type != LogType.Error && type != LogType.Exception && type != LogType.Assert)
                return;

            // Never capture the SDK's own output.
            if (message != null && message.StartsWith(ApmClient.LogPrefix, StringComparison.Ordinal))
                return;

            message = message ?? "";
            stackTrace = stackTrace ?? "";
            long nowMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            // Only an exception logged off the main thread can be the log of an
            // unhandled exception (Unity catches main-thread exceptions itself).
            bool track = !mainThread && type == LogType.Exception;

            // A repeat of a text already seen in this window (up to the numbers
            // in its first line): count it. No parsing, no allocation.
            var key = new RawKey
            {
                MessageHash = MessageKeyHash(message, out int restLength),
                RestLength = restLength,
                StackHash = stackTrace.GetHashCode(),
                StackLength = stackTrace.Length,
                Type = type
            };
            var ctx = GameContext.Current;
            lock (_sync)
            {
                if (_byRawKey.TryGetValue(key, out var known))
                {
                    if (track && IsEchoOfUnhandled(known))
                        return;
                    var occ = NewOccurrenceLocked(nowMs, ctx, null);
                    if (ReferenceEquals(known, Untracked))
                    {
                        CountUntrackedLocked(ref occ, null);
                        return;
                    }
                    var bucket = CountLocked(known, ref occ);
                    if (track)
                        RememberLog(known.Fingerprint, bucket, ref occ);
                    return;
                }
            }

            // First time this text is seen in the window: parse it (allocates),
            // outside the lock.
            var info = Describe(message, stackTrace, type);
            RecordNew(info, key, true, nowMs, ctx, null, track, track);
        }

        /// <summary>
        /// Counts or reports an occurrence of a text not in the repeat table
        /// (parsed into info). Any thread.
        /// </summary>
        private void RecordNew(Entry info, RawKey key, bool cacheKey, long nowMs, ContextSnapshot ctx, string unhandled,
            bool hold, bool track)
        {
            ExceptionEvent first = null;
            lock (_sync)
            {
                if (track && unhandled == null && IsEchoOfUnhandled(info))
                    return;

                // Another thread may have added the text meanwhile.
                Entry entry;
                if (!cacheKey || !_byRawKey.TryGetValue(key, out entry))
                {
                    if (!_entries.TryGetValue(info.Fingerprint, out entry))
                    {
                        entry = _entries.Count < MaxFingerprintsPerWindow ? info : Untracked;
                        if (entry == info)
                            _entries[info.Fingerprint] = info;
                    }
                    if (cacheKey && _byRawKey.Count < MaxRawKeys)
                        _byRawKey[key] = entry;
                }

                var occ = NewOccurrenceLocked(nowMs, ctx, unhandled);
                if (ReferenceEquals(entry, Untracked))
                {
                    CountUntrackedLocked(ref occ, info.Fingerprint);
                    return;
                }

                if (entry == info && _immediateSent < MaxImmediatePerWindow)
                {
                    // New fingerprint: the first occurrence goes to disk right away,
                    // with the time it happened and the breadcrumbs that led to it.
                    _immediateSent++;
                    first = BuildEvent(entry, 1, ref occ, false,
                        SnapshotBreadcrumbsLocked(MaxBreadcrumbsPerEvent, MaxBreadcrumbLength));
                    if (hold)
                    {
                        _held.Add(new HeldEvent { Event = first, Tick = Environment.TickCount });
                        _heldCount = _held.Count;
                        if (track)
                            RememberLog(entry.Fingerprint, first, ref occ);
                        first = null;
                    }
                }
                else
                {
                    // Same group, different text (e.g. another roomId in the
                    // message), or the window's budget for immediate events is
                    // spent: counted, reported at the end of the window.
                    var bucket = CountLocked(entry, ref occ);
                    if (track)
                        RememberLog(entry.Fingerprint, bucket, ref occ);
                }

                if (entry == info)
                {
                    // Record as breadcrumb (once per group and window, not per repeat)
                    RecordBreadcrumbLocked("Exception: " + Truncate(FirstLine(info.Message), 100), nowMs);
                }
            }
            if (first != null)
                Enqueue(first);
        }

        // ---- Unhandled exceptions ----

        private void OnUnhandledException(object sender, UnhandledExceptionEventArgs e)
        {
            try
            {
                HandleUnhandled(e.ExceptionObject as Exception,
                    e.IsTerminating ? UnhandledDomainTerminating : UnhandledDomain, e.IsTerminating);
            }
            catch
            {
                // Never throw from the handler of a dying thread.
            }
        }

        private void OnUnobservedTaskException(object sender, UnobservedTaskExceptionEventArgs e)
        {
            try
            {
                // Not observed: the default behaviour (ignore it) is kept.
                HandleUnhandled(Unwrap(e.Exception), UnhandledTask, false);
            }
            catch
            {
                // Swallowed on purpose.
            }
        }

        private static Exception Unwrap(AggregateException ex)
        {
            // A task's single exception is reported as itself, so it groups with
            // the same exception when it is awaited and logged.
            if (ex != null && ex.InnerExceptions.Count == 1 && ex.InnerExceptions[0] != null)
                return ex.InnerExceptions[0];
            return ex;
        }

        /// <summary>
        /// Records an exception no code caught. Unity may also log it (its
        /// UnhandledExceptionHandler calls Debug.LogException on the same
        /// thread), before or after this: the two are one occurrence. If the log
        /// came first, its occurrence is marked unhandled (it was held for that,
        /// or is moved to an unhandled bucket); otherwise the occurrence is
        /// recorded here and the log that follows is dropped as its echo.
        /// When the process is about to end, everything is written to disk
        /// before returning.
        /// </summary>
        private void HandleUnhandled(Exception ex, string kind, bool terminating)
        {
            if (!_isActive || ex == null)
                return;

            var info = DescribeUnhandled(ex);
            long nowMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            bool mainThread = Environment.CurrentManagedThreadId == _mainThreadId;
            ExceptionEvent release = null;
            bool done = false;
            lock (_sync)
            {
                if (!mainThread && ReferenceEquals(t_logOwner, this) && t_logFingerprint == info.Fingerprint &&
                    unchecked(Environment.TickCount - t_logTick) < EchoWindowMs)
                {
                    done = true;
                    var target = t_logTarget;
                    t_logOwner = null;
                    t_logFingerprint = null;
                    t_logTarget = null;
                    if (target is ExceptionEvent held && RemoveHeldLocked(held))
                    {
                        held.unhandled = kind;
                        release = held;
                    }
                    else if (target is Bucket bucket && t_logWindow == _windowId && bucket.Count > 0 &&
                             _entries.TryGetValue(info.Fingerprint, out var entry))
                    {
                        bucket.Count--;
                        var occ = t_logOccurrence;
                        occ.Unhandled = kind;
                        CountLocked(entry, ref occ);
                    }
                }
            }

            if (release != null)
                Enqueue(release);
            if (!done)
            {
                if (!mainThread)
                {
                    t_echoOwner = this;
                    t_echoFingerprint = info.Fingerprint;
                    t_echoTick = Environment.TickCount;
                }
                RecordNew(info, default, false, nowMs, GameContext.Current, kind, false, false);
            }

            if (terminating)
            {
                FlushPending();
                PersistNow?.Invoke();
            }
        }

        // Caller holds _sync. True (and forgotten) if this thread just reported
        // this exception as unhandled: the log is Unity's echo of it.
        private bool IsEchoOfUnhandled(Entry entry)
        {
            if (!ReferenceEquals(t_echoOwner, this) || t_echoFingerprint == null)
                return false;
            if (unchecked(Environment.TickCount - t_echoTick) >= EchoWindowMs)
            {
                t_echoFingerprint = null;
                return false;
            }
            if (ReferenceEquals(entry, Untracked) || entry.Fingerprint != t_echoFingerprint)
                return false;
            t_echoFingerprint = null;
            return true;
        }

        // Caller holds _sync. This thread logged an exception: remember where it
        // was counted, for an unhandled report that may follow.
        private void RememberLog(string fingerprint, object target, ref Occurrence occ)
        {
            t_logOwner = this;
            t_logFingerprint = fingerprint;
            t_logTick = Environment.TickCount;
            t_logTarget = target;
            t_logOccurrence = occ;
            t_logWindow = _windowId;
        }

        private bool RemoveHeldLocked(ExceptionEvent evt)
        {
            for (int i = 0; i < _held.Count; i++)
            {
                if (ReferenceEquals(_held[i].Event, evt))
                {
                    _held.RemoveAt(i);
                    _heldCount = _held.Count;
                    return true;
                }
            }
            return false;
        }

        // Main thread: enqueue held first occurrences older than HoldMs (all with force).
        private void ReleaseHeld(int now, bool force)
        {
            _released.Clear();
            lock (_sync)
            {
                for (int i = 0; i < _held.Count;)
                {
                    if (force || unchecked(now - _held[i].Tick) >= HoldMs)
                    {
                        _released.Add(_held[i].Event);
                        _held.RemoveAt(i);
                    }
                    else
                    {
                        i++;
                    }
                }
                _heldCount = _held.Count;
            }
            for (int i = 0; i < _released.Count; i++)
                Enqueue(_released[i]);
            _released.Clear();
        }

        // ---- Counting (caller holds _sync) ----

        private Occurrence NewOccurrenceLocked(long nowMs, ContextSnapshot ctx, string unhandled)
        {
            var clock = _clock;
            int generation = clock != null ? clock.Generation : 0;
            if (generation != _seqGeneration)
            {
                // A new session: its first error is number 1.
                _seqGeneration = generation;
                _errorSeq = 0;
            }
            if (_errorSeq < int.MaxValue)
                _errorSeq++;
            _occurrences++;
            return new Occurrence
            {
                TimeMs = nowMs,
                Seq = _errorSeq,
                SessionMs = clock != null ? Math.Max(0, nowMs - clock.StartUnixMs) : 0,
                Ctx = ctx ?? ContextSnapshot.Empty,
                Unhandled = unhandled
            };
        }

        private Bucket CountLocked(Entry e, ref Occurrence occ)
        {
            var b = FindBucket(e, ref occ);
            if (b == null)
            {
                if (e.BucketCount < MaxBucketsPerFingerprint && _bucketsUsed < MaxBucketsPerWindow)
                {
                    if (e.Buckets == null)
                        e.Buckets = new Bucket[MaxBucketsPerFingerprint];
                    b = new Bucket { Ctx = occ.Ctx, Unhandled = occ.Unhandled };
                    e.Buckets[e.BucketCount++] = b;
                    _bucketsUsed++;
                }
                else if (e.Overflow == null || e.Overflow.Count == 0)
                {
                    b = e.Overflow = new Bucket { Ctx = occ.Ctx, Unhandled = occ.Unhandled };
                }
                else
                {
                    // More contexts than are kept apart: keep only what they share.
                    b = e.Overflow;
                    if (!b.Ctx.SameAs(occ.Ctx))
                    {
                        b.Ctx = b.Ctx.Intersect(occ.Ctx);
                        b.Merged = true;
                    }
                    if (!string.Equals(b.Unhandled, occ.Unhandled, StringComparison.Ordinal))
                    {
                        b.Unhandled = null;
                        b.Merged = true;
                    }
                }
            }
            if (b.Count == 0)
            {
                b.FirstMs = occ.TimeMs;
                b.FirstSeq = occ.Seq;
                b.FirstSessionMs = occ.SessionMs;
            }
            b.Count++;
            e.Last = b;
            return b;
        }

        private static Bucket FindBucket(Entry e, ref Occurrence occ)
        {
            var last = e.Last;
            if (last != null && last != e.Overflow && ReferenceEquals(last.Ctx, occ.Ctx) &&
                string.Equals(last.Unhandled, occ.Unhandled, StringComparison.Ordinal))
                return last;
            for (int i = 0; i < e.BucketCount; i++)
            {
                var b = e.Buckets[i];
                if (string.Equals(b.Unhandled, occ.Unhandled, StringComparison.Ordinal) && b.Ctx.SameAs(occ.Ctx))
                    return b;
            }
            return null;
        }

        private void CountUntrackedLocked(ref Occurrence occ, string fingerprint)
        {
            if (_untrackedCount == 0)
                _untrackedFirst = occ;
            if (_untrackedCount < int.MaxValue)
                _untrackedCount++;
            _untrackedTotal++;
            if (fingerprint != null && _untrackedFingerprints.Count < MaxUntrackedFingerprints)
                _untrackedFingerprints.Add(fingerprint);
        }

        /// <summary>
        /// The window's events (buckets, overflow buckets, the summary of
        /// untracked errors, held first occurrences), and a new window. Caller
        /// holds _sync. Null, without allocating, when there is nothing.
        /// </summary>
        private List<ExceptionEvent> CloseWindowLocked()
        {
            List<ExceptionEvent> events = null;
            for (int h = 0; h < _held.Count; h++)
                (events ?? (events = new List<ExceptionEvent>())).Add(_held[h].Event);
            _held.Clear();
            _heldCount = 0;

            foreach (var kvp in _entries)
            {
                var e = kvp.Value;
                for (int i = 0; i < e.BucketCount; i++)
                    AddBucketEvent(ref events, e, e.Buckets[i]);
                if (e.Overflow != null)
                    AddBucketEvent(ref events, e, e.Overflow);
            }

            if (_untrackedCount > 0)
            {
                int kinds = Math.Max(1, _untrackedFingerprints.Count);
                string message = "[OzxApm] " + _untrackedCount.ToString(CultureInfo.InvariantCulture) + " errors of " +
                                 kinds.ToString(CultureInfo.InvariantCulture) + " kinds not reported one by one: more than " +
                                 MaxFingerprintsPerWindow.ToString(CultureInfo.InvariantCulture) + " different errors within " +
                                 (WindowMs / 1000).ToString(CultureInfo.InvariantCulture) + " seconds";
                var summary = new Entry
                {
                    LogType = ExceptionParser.LogTypeError,
                    Message = message,
                    Stack = "",
                    Embedded = ""
                };
                summary.Fingerprint = ExceptionParser.Analyze(summary.LogType, summary.Message, "", "").Fingerprint;
                var occ = _untrackedFirst;
                (events ?? (events = new List<ExceptionEvent>())).Add(BuildEvent(summary, _untrackedCount, ref occ, false, null));
            }

            _entries.Clear();
            _byRawKey.Clear();
            _untrackedFingerprints.Clear();
            _untrackedCount = 0;
            _untrackedFirst = default;
            _immediateSent = 0;
            _bucketsUsed = 0;
            _windowId++;
            Volatile.Write(ref _windowStartTick, Environment.TickCount);
            if (events != null)
                _eventsEnqueued += events.Count;
            return events;
        }

        private static void AddBucketEvent(ref List<ExceptionEvent> events, Entry e, Bucket b)
        {
            if (b.Count <= 0)
                return;
            var occ = new Occurrence
            {
                TimeMs = b.FirstMs,
                Seq = b.FirstSeq,
                SessionMs = b.FirstSessionMs,
                Ctx = b.Ctx,
                Unhandled = b.Unhandled
            };
            (events ?? (events = new List<ExceptionEvent>())).Add(BuildEvent(e, b.Count, ref occ, b.Merged, null));
        }

        /// <summary>
        /// Builds what is sent for a log and its fingerprint. An error or
        /// assert whose message embeds an exception (OZX's GameEventBus logs
        /// "[GameEventBus] Error handling event X: {ex}") gets that exception
        /// extracted from the full message first; the fingerprint is computed
        /// from exactly the fields that are sent, as the server will.
        /// </summary>
        private static Entry Describe(string message, string stackTrace, LogType type)
        {
            string logType = type == LogType.Exception ? ExceptionParser.LogTypeException
                : type == LogType.Assert ? ExceptionParser.LogTypeAssert
                : ExceptionParser.LogTypeError;
            string embedded = "";
            if (type != LogType.Exception)
            {
                int i = ExceptionParser.FindEmbedded(message);
                if (i >= 0)
                    embedded = ExceptionParser.Compact(message.Substring(i, Math.Min(message.Length - i, MaxEmbeddedSourceLength)), MaxEmbeddedLength);
            }
            var info = new Entry
            {
                LogType = logType,
                Message = TruncateMessage(message, MaxMessageLength),
                Stack = TruncateStack(stackTrace, MaxStackLength),
                Embedded = embedded
            };
            info.Fingerprint = ExceptionParser.Analyze(logType, info.Message, info.Stack, embedded).Fingerprint;
            return info;
        }

        /// <summary>
        /// An unhandled exception as it is sent: its Exception.ToString() as the
        /// embedded exception of an error whose message is the first line, so it
        /// is parsed (type, frames, inner exceptions) and grouped like the same
        /// exception logged by the game.
        /// </summary>
        private static Entry DescribeUnhandled(Exception ex)
        {
            string text;
            try
            {
                text = ex.ToString();
            }
            catch
            {
                text = ex.GetType().FullName + ": (ToString failed)";
            }
            if (string.IsNullOrEmpty(text))
                text = ex.GetType().FullName;
            string embedded = ExceptionParser.Compact(text.Length > MaxEmbeddedSourceLength ? text.Substring(0, MaxEmbeddedSourceLength) : text,
                MaxEmbeddedLength);
            var info = new Entry
            {
                LogType = ExceptionParser.LogTypeError,
                Message = TruncateMessage(FirstLine(text), MaxMessageLength),
                Stack = "",
                Embedded = embedded
            };
            info.Fingerprint = ExceptionParser.Analyze(info.LogType, info.Message, info.Stack, embedded).Fingerprint;
            return info;
        }

        private static ExceptionEvent BuildEvent(Entry info, int count, ref Occurrence occ, bool merged, List<string> breadcrumbs)
        {
            // Repeat counts carry the same text as the first occurrence, so the
            // server computes the same fingerprint for them.
            return new ExceptionEvent
            {
                timestamp = occ.TimeMs,
                fingerprint = info.Fingerprint,
                log_type = info.LogType,
                message = info.Message,
                stack = info.Stack,
                embedded_exception = info.Embedded,
                count = count,
                breadcrumbs = breadcrumbs,
                error_seq = occ.Seq,
                session_ms = occ.SessionMs,
                unhandled = occ.Unhandled,
                ctx_merged = merged,
                // The context when it happened (not when it is enqueued).
                ctx = occ.Ctx
            };
        }

        private void EnqueueAll(List<ExceptionEvent> events)
        {
            if (events == null)
                return;
            for (int i = 0; i < events.Count; i++)
                Enqueue(events[i]);
        }

        // Never called with _sync held.
        private void Enqueue(ExceptionEvent evt)
        {
            if (EnqueueForTests != null)
            {
                EnqueueForTests(evt);
                return;
            }
            if (_client != null)
                _client.EnqueueEvent(evt);
        }

        private static string FirstLine(string s)
        {
            int i = s.IndexOf('\n');
            return i >= 0 ? s.Substring(0, i).TrimEnd('\r') : s;
        }

        private static string Truncate(string s, int maxLength)
        {
            if (s.Length <= maxLength)
                return s;
            int n = maxLength;
            if (char.IsHighSurrogate(s[n - 1]))
                n--;
            return s.Substring(0, n);
        }

        private static string TruncateMessage(string message, int maxLength)
        {
            if (string.IsNullOrEmpty(message))
                return "";
            if (message.Length <= maxLength)
                return message;
            return Truncate(message, maxLength - 3) + "...";
        }

        private static string TruncateStack(string stack, int maxLength)
        {
            if (string.IsNullOrEmpty(stack))
                return "";
            if (stack.Length <= maxLength)
                return stack;

            // Keep the beginning (most important) and note truncation
            return Truncate(stack, maxLength - 20) + "\n[truncated...]";
        }
    }
}
