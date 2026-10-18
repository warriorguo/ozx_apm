using System;
using System.Collections.Generic;
using System.Text;
using UnityEngine;
using OzxApm.Collectors;

namespace OzxApm.Core
{
    /// <summary>
    /// What happened before an exception, a crash or a jank (APM-6): one time
    /// line made of three rings, merged in the order things were recorded when
    /// an event takes a snapshot.
    ///
    /// - Breadcrumbs (MaxBreadcrumbs): custom events (ApmClient.TrackEvent,
    ///   with their properties), scene loads, exceptions (first occurrence of
    ///   a group per window) and ApmClient.RecordBreadcrumb texts.
    /// - Recent Warning lines (MaxWarningLines) and recent Error/Assert lines
    ///   (MaxErrorLines) the game logs on any thread (Unity's log callback,
    ///   shared with ExceptionCollector through ILogSink); not plain Log lines,
    ///   not exceptions (those are breadcrumbs), not the SDK's own output. A
    ///   line of the same shape (equal up to the digits, e.g. another roomId)
    ///   as one of the LogDedupeDepth most recent lines of its ring is not
    ///   stored again: the earlier one counts it ("(x37 until 12:00:05.120)"),
    ///   so a line logged every frame does not push older ones out.
    ///
    /// The rings are separate so that warning spam cannot evict the errors or
    /// the custom events, and the other way round.
    ///
    /// Cost: recording stores references (the game's or Unity's strings) and
    /// the time under a lock: no allocation, no formatting. A new log line
    /// longer than MaxStoredLogLength is stored as a cut copy (only such lines
    /// allocate, and only when they are not a repeat), so at most
    /// (MaxWarningLines + MaxErrorLines) x MaxStoredLogLength characters are
    /// kept alive. Text is formatted, cut to its length limit and has the
    /// query strings of http(s) URLs removed only when an event takes a
    /// snapshot. Thread-safe; the lock is a leaf (nothing is logged or called
    /// while it is held).
    /// </summary>
    internal sealed class Timeline : ILogSink
    {
        internal const int MaxBreadcrumbs = 50;
        internal const int MaxWarningLines = 24;
        internal const int MaxErrorLines = 12;
        internal const int MaxLogLines = MaxWarningLines + MaxErrorLines;
        internal const int LogDedupeDepth = 8;
        internal const int MaxStoredLogLength = 4096;
        // Characters of a log line hashed for the shape comparison.
        private const int MaxShapeChars = 1024;

        private const byte KindText = 0;
        private const byte KindEvent = 1;

        private readonly object _lock = new object();

        private readonly long[] _crumbTimes = new long[MaxBreadcrumbs];
        private readonly string[] _crumbTexts = new string[MaxBreadcrumbs];
        private readonly string[][] _crumbProps = new string[MaxBreadcrumbs][];
        private readonly byte[] _crumbKinds = new byte[MaxBreadcrumbs];
        private readonly long[] _crumbSeqs = new long[MaxBreadcrumbs];
        private int _crumbStart;
        private int _crumbCount;

        private readonly LogRing _warnings = new LogRing(MaxWarningLines);
        private readonly LogRing _errors = new LogRing(MaxErrorLines);
        // Order of recording across all rings: the merge order.
        private long _seq;
        private long _logLinesSeen;

        private readonly StringBuilder _sb = new StringBuilder(256);
        // Snapshot scratch (under _lock).
        private readonly int[] _pickCrumbs = new int[MaxBreadcrumbs];
        private readonly LogRing[] _pickLogRing = new LogRing[MaxLogLines];
        private readonly int[] _pickLogIndex = new int[MaxLogLines];

        /// <summary>One ring of log lines (guarded by the Timeline's lock).</summary>
        private sealed class LogRing
        {
            public readonly int Capacity;
            public readonly long[] Times, LastTimes, Seqs;
            public readonly string[] Texts;
            public readonly int[] Counts;
            public readonly ulong[] Shapes;
            public readonly LogType[] Types;
            public int Start, Count;

            public LogRing(int capacity)
            {
                Capacity = capacity;
                Times = new long[capacity];
                LastTimes = new long[capacity];
                Seqs = new long[capacity];
                Texts = new string[capacity];
                Counts = new int[capacity];
                Shapes = new ulong[capacity];
                Types = new LogType[capacity];
            }

            public int At(int k) => (Start + k) % Capacity;

            public void Clear()
            {
                Array.Clear(Texts, 0, Capacity);
                Start = Count = 0;
            }
        }

        /// <summary>Warning/Error/Assert lines recorded (repeats counted) since start.</summary>
        internal long LogLinesSeen
        {
            get
            {
                lock (_lock)
                    return _logLinesSeen;
            }
        }

        internal static long NowMs() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

        /// <summary>A breadcrumb text (shown as is). No allocation. Any thread.</summary>
        public void RecordText(string text)
        {
            if (string.IsNullOrEmpty(text))
                return;
            lock (_lock)
                AddCrumbLocked(NowMs(), KindText, text, null);
            CrashJournal.NotifyChanged();
        }

        /// <summary>A breadcrumb text with the time it happened.</summary>
        public void RecordText(string text, long timeMs)
        {
            if (string.IsNullOrEmpty(text))
                return;
            lock (_lock)
                AddCrumbLocked(timeMs, KindText, text, null);
            CrashJournal.NotifyChanged();
        }

        /// <summary>
        /// A custom event: its name and properties (key, value pairs; the array
        /// is kept as is and must not change afterwards). No allocation.
        /// </summary>
        public void RecordEvent(string name, string[] props, long timeMs)
        {
            if (string.IsNullOrEmpty(name))
                return;
            lock (_lock)
                AddCrumbLocked(timeMs, KindEvent, name, props);
            CrashJournal.NotifyChanged();
        }

        private void AddCrumbLocked(long timeMs, byte kind, string text, string[] props)
        {
            int i = (_crumbStart + _crumbCount) % MaxBreadcrumbs;
            if (_crumbCount == MaxBreadcrumbs)
                _crumbStart = (_crumbStart + 1) % MaxBreadcrumbs;
            else
                _crumbCount++;
            _crumbTimes[i] = timeMs;
            _crumbSeqs[i] = ++_seq;
            _crumbKinds[i] = kind;
            _crumbTexts[i] = text;
            _crumbProps[i] = props;
        }

        /// <summary>
        /// ILogSink: called by ExceptionCollector's log callback for every log
        /// line, on the logging thread. Records Warning, Error and Assert lines;
        /// true if the line is on the time line. Never throws, never logs; no
        /// allocation unless a new line is longer than MaxStoredLogLength.
        /// </summary>
        public bool OnLog(string message, string stackTrace, LogType type, bool mainThread)
        {
            if (type != LogType.Warning && type != LogType.Error && type != LogType.Assert)
                return false;
            if (string.IsNullOrEmpty(message) || message.StartsWith(ApmClient.LogPrefix, StringComparison.Ordinal))
                return false;
            ulong shape = ShapeHash(message);
            var ring = type == LogType.Warning ? _warnings : _errors;
            bool repeated = false;
            lock (_lock)
            {
                long now = NowMs();
                _logLinesSeen++;
                int depth = Math.Min(LogDedupeDepth, ring.Count);
                for (int k = 0; k < depth; k++)
                {
                    int j = ring.At(ring.Count - 1 - k);
                    if (ring.Shapes[j] == shape && ring.Types[j] == type)
                    {
                        if (ring.Counts[j] < int.MaxValue)
                            ring.Counts[j]++;
                        ring.LastTimes[j] = now;
                        repeated = true;
                        break;
                    }
                }
                if (!repeated)
                {
                    int i = ring.At(ring.Count);
                    if (ring.Count == ring.Capacity)
                        ring.Start = (ring.Start + 1) % ring.Capacity;
                    else
                        ring.Count++;
                    ring.Times[i] = now;
                    ring.LastTimes[i] = now;
                    ring.Seqs[i] = ++_seq;
                    // Only a new, very long line is copied (a repeat is only counted).
                    ring.Texts[i] = message.Length > MaxStoredLogLength ? message.Substring(0, MaxStoredLogLength) : message;
                    ring.Counts[i] = 1;
                    ring.Shapes[i] = shape;
                    ring.Types[i] = type;
                }
            }
            // After the lock (a leaf): the crash journal rewrites itself (APM-7).
            CrashJournal.NotifyChanged();
            return true;
        }

        /// <summary>Forgets everything (technical consent denied).</summary>
        public void Clear()
        {
            lock (_lock)
            {
                Array.Clear(_crumbTexts, 0, MaxBreadcrumbs);
                Array.Clear(_crumbProps, 0, MaxBreadcrumbs);
                _crumbStart = _crumbCount = 0;
                _warnings.Clear();
                _errors.Clear();
            }
        }

        /// <summary>
        /// The time line as sent with an event, oldest first: the most recent
        /// maxBreadcrumbs breadcrumbs and maxLogs log lines (warnings and errors
        /// together) at or after sinceMs, merged in the order they were
        /// recorded, as "[HH:mm:ss.fff] text" (UTC), breadcrumbs cut to
        /// maxBreadcrumbLength characters, log lines to maxLogLength. A log line
        /// recorded once whose text is excludeLog (the same string instance:
        /// the error being reported) is left out. Allocates the list and its strings.
        /// </summary>
        public List<string> Snapshot(int maxBreadcrumbs, int maxLogs, int maxBreadcrumbLength, int maxLogLength,
            long sinceMs = long.MinValue, string excludeLog = null)
        {
            lock (_lock)
            {
                PickLocked(maxBreadcrumbs, maxLogs, sinceMs, excludeLog, out int nc, out int nl);
                var list = new List<string>(nc + nl);
                // Both picks are newest first; merge oldest first.
                int c = nc - 1, l = nl - 1;
                while (c >= 0 || l >= 0)
                {
                    bool takeCrumb = l < 0 || (c >= 0 && _crumbSeqs[_pickCrumbs[c]] < _pickLogRing[l].Seqs[_pickLogIndex[l]]);
                    if (takeCrumb)
                    {
                        FormatCrumbLocked(_pickCrumbs[c--], maxBreadcrumbLength);
                    }
                    else
                    {
                        FormatLogLocked(_pickLogRing[l], _pickLogIndex[l], maxLogLength);
                        _pickLogRing[l--] = null;
                    }
                    list.Add(_sb.ToString());
                }
                return list;
            }
        }

        /// <summary>
        /// The time line as Snapshot formats it, appended to dst: each line as
        /// prefix + line (passed through append, which escapes it) + '\n'.
        /// Allocates nothing (APM-7: the crash journal, written several times a
        /// second at most, on its own thread). Returns the number of lines.
        /// </summary>
        internal int AppendSnapshot(StringBuilder dst, string prefix, Action<StringBuilder, StringBuilder> append,
            int maxBreadcrumbs, int maxLogs, int maxBreadcrumbLength, int maxLogLength)
        {
            lock (_lock)
            {
                PickLocked(maxBreadcrumbs, maxLogs, long.MinValue, null, out int nc, out int nl);
                int c = nc - 1, l = nl - 1, n = 0;
                while (c >= 0 || l >= 0)
                {
                    bool takeCrumb = l < 0 || (c >= 0 && _crumbSeqs[_pickCrumbs[c]] < _pickLogRing[l].Seqs[_pickLogIndex[l]]);
                    if (takeCrumb)
                    {
                        FormatCrumbLocked(_pickCrumbs[c--], maxBreadcrumbLength);
                    }
                    else
                    {
                        FormatLogLocked(_pickLogRing[l], _pickLogIndex[l], maxLogLength);
                        _pickLogRing[l--] = null;
                    }
                    dst.Append(prefix);
                    append(dst, _sb);
                    dst.Append('\n');
                    n++;
                }
                return n;
            }
        }

        // Picks the most recent breadcrumbs and log lines into the scratch
        // arrays, newest first (under _lock).
        private void PickLocked(int maxBreadcrumbs, int maxLogs, long sinceMs, string excludeLog, out int ncOut, out int nlOut)
        {
            int nc = 0;
            for (int k = _crumbCount - 1; k >= 0 && nc < maxBreadcrumbs; k--)
            {
                int i = (_crumbStart + k) % MaxBreadcrumbs;
                if (_crumbTimes[i] < sinceMs)
                    continue;
                _pickCrumbs[nc++] = i;
            }

            // The newest maxLogs lines of both log rings, newest first.
            int nl = 0;
            int kw = _warnings.Count - 1, ke = _errors.Count - 1;
            while (nl < maxLogs && nl < MaxLogLines && (kw >= 0 || ke >= 0))
            {
                LogRing ring;
                int idx;
                if (ke < 0 || (kw >= 0 && _warnings.Seqs[_warnings.At(kw)] > _errors.Seqs[_errors.At(ke)]))
                {
                    ring = _warnings;
                    idx = _warnings.At(kw--);
                }
                else
                {
                    ring = _errors;
                    idx = _errors.At(ke--);
                }
                // Not break: a line counted again recently may sit before
                // newer ones (a ring is in order of first occurrence).
                if (ring.LastTimes[idx] < sinceMs)
                    continue;
                if (excludeLog != null && ring.Counts[idx] == 1 && ReferenceEquals(ring.Texts[idx], excludeLog))
                    continue;
                _pickLogRing[nl] = ring;
                _pickLogIndex[nl++] = idx;
            }
            ncOut = nc;
            nlOut = nl;
        }

        // Formats a breadcrumb into _sb (under _lock).
        private void FormatCrumbLocked(int i, int maxLength)
        {
            var sb = _sb;
            sb.Length = 0;
            AppendTime(sb, _crumbTimes[i]);
            if (_crumbKinds[i] == KindEvent)
            {
                sb.Append("Event: ").Append(_crumbTexts[i]);
                var props = _crumbProps[i];
                if (props != null)
                {
                    for (int p = 0; p + 1 < props.Length && sb.Length < maxLength; p += 2)
                        sb.Append(' ').Append(props[p]).Append('=').Append(props[p + 1]);
                }
            }
            else
            {
                AppendCut(sb, _crumbTexts[i], maxLength);
            }
            StripUrlQueries(sb, 15);
            Cut(sb, maxLength);
        }

        // Formats a log line into _sb (under _lock).
        private void FormatLogLocked(LogRing ring, int i, int maxLength)
        {
            var sb = _sb;
            sb.Length = 0;
            AppendTime(sb, ring.Times[i]);
            var type = ring.Types[i];
            sb.Append(type == LogType.Warning ? "Warning: " : type == LogType.Assert ? "Assert: " : "Error: ");
            int count = ring.Counts[i];
            // " (x2147483647 until 00:00:00.000)": at most 33 characters.
            int room = count > 1 ? maxLength - 34 : maxLength;
            // A query string removed below may make room: append generously.
            AppendCut(sb, ring.Texts[i], room > int.MaxValue / 2 ? room : room * 2);
            StripUrlQueries(sb, 15);
            Cut(sb, room);
            if (count > 1)
            {
                sb.Append(" (x");
                AppendDigits(sb, count);
                sb.Append(" until ");
                AppendClock(sb, ring.LastTimes[i]);
                sb.Append(')');
            }
        }

        /// <summary>Appends a non-negative number without allocating (StringBuilder.Append(int) does on Mono).</summary>
        internal static void AppendDigits(StringBuilder sb, long value)
        {
            if (value < 0)
            {
                if (value == long.MinValue)
                {
                    sb.Append("-9223372036854775808");
                    return;
                }
                sb.Append('-');
                value = -value;
            }
            long div = 1;
            while (value / div >= 10)
                div *= 10;
            for (; div > 0; div /= 10)
                sb.Append((char)('0' + value / div % 10));
        }

        /// <summary>
        /// Removes the query string and fragment of every http(s) URL in
        /// sb[from..] (privacy: tokens in UnityWebRequest error messages; the
        /// same rule as the network log's URLs): "https://h/p?token=x" becomes
        /// "https://h/p?...".
        /// </summary>
        internal static void StripUrlQueries(StringBuilder sb, int from)
        {
            for (int i = Math.Max(0, from); i + 7 < sb.Length; i++)
            {
                if ((sb[i] | 0x20) != 'h' || !(StartsWith(sb, i, "http://") || StartsWith(sb, i, "https://")))
                    continue;
                int j = i + 7;
                while (j < sb.Length && !EndsUrl(sb[j]) && sb[j] != '?' && sb[j] != '#')
                    j++;
                if (j < sb.Length && (sb[j] == '?' || sb[j] == '#'))
                {
                    int end = j + 1;
                    while (end < sb.Length && !EndsUrl(sb[end]))
                        end++;
                    ReplaceInPlace(sb, j, end, "?...");
                    j += 4;
                }
                i = j;
            }
        }

        // Replaces sb[start..end) with s by moving the characters after it:
        // no allocation while the builder has the capacity (StringBuilder's
        // Remove + Insert allocate on Mono; the crash journal formats the
        // time line several times a second, APM-7).
        private static void ReplaceInPlace(StringBuilder sb, int start, int end, string s)
        {
            int removed = end - start, delta = s.Length - removed, len = sb.Length;
            if (delta < 0)
            {
                for (int k = end; k < len; k++)
                    sb[k + delta] = sb[k];
                sb.Length = len + delta;
            }
            else if (delta > 0)
            {
                for (int k = 0; k < delta; k++)
                    sb.Append('\0');
                for (int k = len - 1; k >= end; k--)
                    sb[k + delta] = sb[k];
            }
            for (int k = 0; k < s.Length; k++)
                sb[start + k] = s[k];
        }

        private static bool StartsWith(StringBuilder sb, int at, string s)
        {
            if (at + s.Length > sb.Length)
                return false;
            for (int k = 0; k < s.Length; k++)
            {
                if (char.ToLowerInvariant(sb[at + k]) != s[k])
                    return false;
            }
            return true;
        }

        private static bool EndsUrl(char c) => char.IsWhiteSpace(c) || c == '"' || c == '\'' || c == '<' || c == '>' || c == ')' || c == ']';

        // Appends at most what fits in maxLength (the rest would be cut anyway).
        private static void AppendCut(StringBuilder sb, string s, int maxLength)
        {
            int room = maxLength - sb.Length + 1;
            if (room <= 0)
                return;
            sb.Append(s, 0, Math.Min(s.Length, room));
        }

        private static void Cut(StringBuilder sb, int maxLength)
        {
            if (sb.Length <= maxLength)
                return;
            int n = Math.Max(0, maxLength);
            if (n > 0 && char.IsHighSurrogate(sb[n - 1]))
                n--;
            sb.Length = n;
        }

        private static void AppendTime(StringBuilder sb, long ms)
        {
            sb.Append('[');
            AppendClock(sb, ms);
            sb.Append("] ");
        }

        // HH:mm:ss.fff of a Unix time in milliseconds (UTC), without allocating.
        private static void AppendClock(StringBuilder sb, long ms)
        {
            long dayMs = ((ms % 86400000L) + 86400000L) % 86400000L;
            int h = (int)(dayMs / 3600000L), m = (int)(dayMs / 60000L % 60), s = (int)(dayMs / 1000L % 60), f = (int)(dayMs % 1000);
            sb.Append((char)('0' + h / 10)).Append((char)('0' + h % 10)).Append(':')
              .Append((char)('0' + m / 10)).Append((char)('0' + m % 10)).Append(':')
              .Append((char)('0' + s / 10)).Append((char)('0' + s % 10)).Append('.')
              .Append((char)('0' + f / 100)).Append((char)('0' + f / 10 % 10)).Append((char)('0' + f % 10));
        }

        /// <summary>
        /// Hash of the text in which every run of digits counts as one '#'
        /// (the same idea as ExceptionCollector's repeat key), over at most
        /// MaxShapeChars characters, with the length class of the rest. No allocation.
        /// </summary>
        internal static ulong ShapeHash(string s)
        {
            uint h1 = 5381, h2 = 52711;
            int n = Math.Min(s.Length, MaxShapeChars);
            bool inDigits = false, odd = false;
            for (int i = 0; i < n; i++)
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
            if (s.Length > MaxShapeChars)
                h1 = unchecked(((h1 << 5) + h1) ^ (uint)(s.Length / 64));
            return ((ulong)h1 << 32) | h2;
        }
    }
}
