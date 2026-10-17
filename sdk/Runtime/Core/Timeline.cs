using System;
using System.Collections.Generic;
using System.Text;
using UnityEngine;
using OzxApm.Collectors;

namespace OzxApm.Core
{
    /// <summary>
    /// What happened before an exception, a crash or a jank (APM-6): one time
    /// line made of two rings, merged by time when an event is sent.
    ///
    /// - Breadcrumbs (MaxBreadcrumbs): custom events (ApmClient.TrackEvent,
    ///   with their properties), scene loads, exceptions (first occurrence of
    ///   a group per window) and ApmClient.RecordBreadcrumb texts.
    /// - Recent log lines (MaxLogLines): every Warning, Error and Assert line
    ///   the game logs on any thread (Unity's log callback, shared with
    ///   ExceptionCollector through ILogSink); not plain Log lines, not
    ///   exceptions (those are breadcrumbs), not the SDK's own output. A line
    ///   of the same shape (equal up to the digits, e.g. another roomId) as
    ///   one of the LogDedupeDepth most recent lines is not stored again: the
    ///   earlier one counts it ("(x37 until 12:00:05.120)"), so a line logged
    ///   every frame does not push older ones out. Errors cannot push
    ///   warnings out of the ring beyond that: both share it.
    ///
    /// The two rings are separate so that log spam cannot evict the custom
    /// events, and custom-event spam cannot evict the warnings.
    ///
    /// Cost: recording stores references (the game's or Unity's strings) and
    /// the time under a lock: no allocation, no formatting. A log line longer
    /// than MaxStoredLogLength is stored as a cut copy (only such lines
    /// allocate), so at most MaxLogLines x MaxStoredLogLength characters are
    /// kept alive. Text is formatted and cut to its length limit only when an
    /// event takes a snapshot. Thread-safe; the lock is a leaf (nothing is
    /// logged or called while it is held).
    /// </summary>
    internal sealed class Timeline : ILogSink
    {
        internal const int MaxBreadcrumbs = 50;
        internal const int MaxLogLines = 32;
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

        private readonly long[] _logTimes = new long[MaxLogLines];
        private readonly long[] _logLastTimes = new long[MaxLogLines];
        private readonly string[] _logTexts = new string[MaxLogLines];
        private readonly int[] _logCounts = new int[MaxLogLines];
        private readonly ulong[] _logShapes = new ulong[MaxLogLines];
        private readonly LogType[] _logTypes = new LogType[MaxLogLines];
        private readonly long[] _logSeqs = new long[MaxLogLines];
        // Order of recording across both rings: the merge order.
        private long _seq;
        private int _logStart;
        private int _logCount;
        private long _logLinesSeen;

        private readonly StringBuilder _sb = new StringBuilder(256);
        // Snapshot merge scratch (under _lock).
        private readonly int[] _pickCrumbs = new int[MaxBreadcrumbs];
        private readonly int[] _pickLogs = new int[MaxLogLines];

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
        }

        /// <summary>A breadcrumb text with the time it happened.</summary>
        public void RecordText(string text, long timeMs)
        {
            if (string.IsNullOrEmpty(text))
                return;
            lock (_lock)
                AddCrumbLocked(timeMs, KindText, text, null);
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
        /// allocation unless the line is longer than MaxStoredLogLength.
        /// </summary>
        public bool OnLog(string message, string stackTrace, LogType type, bool mainThread)
        {
            if (type != LogType.Warning && type != LogType.Error && type != LogType.Assert)
                return false;
            if (string.IsNullOrEmpty(message) || message.StartsWith(ApmClient.LogPrefix, StringComparison.Ordinal))
                return false;
            ulong shape = ShapeHash(message);
            string stored = message.Length > MaxStoredLogLength ? message.Substring(0, MaxStoredLogLength) : message;
            lock (_lock)
            {
                long now = NowMs();
                _logLinesSeen++;
                int depth = Math.Min(LogDedupeDepth, _logCount);
                for (int k = 0; k < depth; k++)
                {
                    int j = (_logStart + _logCount - 1 - k) % MaxLogLines;
                    if (_logShapes[j] == shape && _logTypes[j] == type)
                    {
                        if (_logCounts[j] < int.MaxValue)
                            _logCounts[j]++;
                        _logLastTimes[j] = now;
                        return true;
                    }
                }
                int i = (_logStart + _logCount) % MaxLogLines;
                if (_logCount == MaxLogLines)
                    _logStart = (_logStart + 1) % MaxLogLines;
                else
                    _logCount++;
                _logTimes[i] = now;
                _logSeqs[i] = ++_seq;
                _logLastTimes[i] = now;
                _logTexts[i] = stored;
                _logCounts[i] = 1;
                _logShapes[i] = shape;
                _logTypes[i] = type;
            }
            return true;
        }

        /// <summary>Forgets everything (technical consent denied).</summary>
        public void Clear()
        {
            lock (_lock)
            {
                Array.Clear(_crumbTexts, 0, MaxBreadcrumbs);
                Array.Clear(_crumbProps, 0, MaxBreadcrumbs);
                Array.Clear(_logTexts, 0, MaxLogLines);
                _crumbStart = _crumbCount = 0;
                _logStart = _logCount = 0;
            }
        }

        /// <summary>
        /// The time line as sent with an event, oldest first: the most recent
        /// maxBreadcrumbs breadcrumbs and maxLogs log lines at or after sinceMs,
        /// merged in the order they were recorded, as "[HH:mm:ss.fff] text" (UTC), breadcrumbs cut to
        /// maxBreadcrumbLength characters, log lines to maxLogLength. A log line
        /// recorded once whose text is excludeLog (the same string instance:
        /// the error being reported) is left out. Allocates the list and its strings.
        /// </summary>
        public List<string> Snapshot(int maxBreadcrumbs, int maxLogs, int maxBreadcrumbLength, int maxLogLength,
            long sinceMs = long.MinValue, string excludeLog = null)
        {
            lock (_lock)
            {
                int nc = 0;
                for (int k = _crumbCount - 1; k >= 0 && nc < maxBreadcrumbs; k--)
                {
                    int i = (_crumbStart + k) % MaxBreadcrumbs;
                    if (_crumbTimes[i] < sinceMs)
                        continue;
                    _pickCrumbs[nc++] = i;
                }
                int nl = 0;
                for (int k = _logCount - 1; k >= 0 && nl < maxLogs; k--)
                {
                    int i = (_logStart + k) % MaxLogLines;
                    // Not break: a line counted again recently may sit before
                    // newer ones (the ring is in order of first occurrence).
                    if (_logLastTimes[i] < sinceMs)
                        continue;
                    if (excludeLog != null && _logCounts[i] == 1 && ReferenceEquals(_logTexts[i], excludeLog))
                        continue;
                    _pickLogs[nl++] = i;
                }

                var list = new List<string>(nc + nl);
                // Both picks are newest first; merge oldest first.
                int c = nc - 1, l = nl - 1;
                while (c >= 0 || l >= 0)
                {
                    bool takeCrumb = l < 0 || (c >= 0 && CrumbFirst(_pickCrumbs[c], _pickLogs[l]));
                    if (takeCrumb)
                        list.Add(FormatCrumbLocked(_pickCrumbs[c--], maxBreadcrumbLength));
                    else
                        list.Add(FormatLogLocked(_pickLogs[l--], maxLogLength));
                }
                return list;
            }
        }

        // In the order they were recorded (one lock for both rings), which is
        // their real order; the times only label them (a breadcrumb may carry
        // a time read just before the lock).
        private bool CrumbFirst(int crumb, int log) => _crumbSeqs[crumb] < _logSeqs[log];

        private string FormatCrumbLocked(int i, int maxLength)
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
            Cut(sb, maxLength);
            return sb.ToString();
        }

        private string FormatLogLocked(int i, int maxLength)
        {
            var sb = _sb;
            sb.Length = 0;
            AppendTime(sb, _logTimes[i]);
            var type = _logTypes[i];
            sb.Append(type == LogType.Warning ? "Warning: " : type == LogType.Assert ? "Assert: " : "Error: ");
            int count = _logCounts[i];
            // " (x2147483647 until 00:00:00.000)": at most 33 characters.
            int room = count > 1 ? maxLength - 34 : maxLength;
            AppendCut(sb, _logTexts[i], room);
            Cut(sb, room);
            if (count > 1)
            {
                sb.Append(" (x").Append(count).Append(" until ");
                AppendClock(sb, _logLastTimes[i]);
                sb.Append(')');
            }
            return sb.ToString();
        }

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
