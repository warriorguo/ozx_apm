using System;
using System.Collections.Generic;
using System.Globalization;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Captures unhandled exceptions and errors
    /// </summary>
    public class ExceptionCollector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;

        // Deduplication
        private readonly Dictionary<string, ExceptionInfo> _recentExceptions = new Dictionary<string, ExceptionInfo>();
        private const int MaxRecentExceptions = 100;
        private const float DedupeWindowSeconds = 60f;
        private float _lastCleanupTime;

        // What is sent (APM-4). The embedded exception is extracted from the
        // full message before the message is cut, and kept under the stack's
        // limit, not the message's.
        internal const int MaxMessageLength = 500;
        internal const int MaxStackLength = 8000;
        internal const int MaxEmbeddedLength = 8000;

        // Fingerprint of each distinct log text seen in the current window, so
        // that a repeat is counted without parsing it again or allocating
        // (APM-3/APM-4: an error logged every frame must stay cheap). Keyed by
        // hashes of the text, cleared with the window.
        private readonly Dictionary<RawKey, string> _fingerprintByRawKey =
            new Dictionary<RawKey, string>(MaxRawKeys, RawKeyComparer.Instance);
        private const int MaxRawKeys = 256;

        // Breadcrumbs for crash context: a ring of (time, message), formatted
        // only when they are sent, so recording one does not allocate.
        private const int MaxBreadcrumbs = 50;
        private readonly long[] _breadcrumbTimes = new long[MaxBreadcrumbs];
        private readonly string[] _breadcrumbMessages = new string[MaxBreadcrumbs];
        private int _breadcrumbStart;
        private int _breadcrumbCount;
        // Sent with the first occurrence of an exception: the most recent ones,
        // each cut to a bounded length (at most ~4.5 KB per event).
        internal const int MaxBreadcrumbsPerEvent = 20;
        internal const int MaxBreadcrumbLength = 200;

        // Within a dedupe window, the first occurrence of a fingerprint is enqueued
        // (and so persisted) immediately with count=1. Further occurrences are only
        // counted and enqueued as one event with count=N at the end of the window,
        // timestamped with the first repeat. sum(count) stays exact, and a crash
        // right after an exception does not lose it.
        private class ExceptionInfo
        {
            public string Fingerprint;
            public string LogType;
            public string Message;
            public string Stack;
            public string Embedded;
            public int RepeatCount;
            public long RepeatFirstSeenMs;
            public ContextSnapshot RepeatContext; // game context at the first repeat
        }

        // The whole text is hashed (string.GetHashCode, native on Mono and
        // IL2CPP): what makes two wrapped exceptions different (the outer
        // exception's frames) can be anywhere in a long message. Two different
        // texts share an entry only if both 32-bit hashes and both lengths are
        // equal.
        private struct RawKey
        {
            public int MessageHash;
            public int StackHash;
            public int MessageLength;
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
                       a.MessageLength == b.MessageLength && a.StackLength == b.StackLength && a.Type == b.Type;
            }

            public int GetHashCode(RawKey k)
            {
                return unchecked(k.MessageHash ^ (k.StackHash * 31) ^ k.MessageLength ^ (int)k.Type);
            }
        }

        public bool IsActive => _isActive;

        // Test seams: receive the events instead of ApmClient.EnqueueEvent, and
        // activate without subscribing to Unity's log callback.
        internal Action<ExceptionEvent> EnqueueForTests;
        internal void ActivateForTests() => _isActive = true;

        public ExceptionCollector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _lastCleanupTime = Time.realtimeSinceStartup;

            // Subscribe to Unity's log callback
            Application.logMessageReceived += OnLogMessage;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            // Periodically flush deduplicated exceptions and cleanup
            float now = Time.realtimeSinceStartup;
            if (now - _lastCleanupTime > DedupeWindowSeconds)
            {
                FlushExceptions();
                _lastCleanupTime = now;
            }
        }

        public void Stop()
        {
            _isActive = false;
            Application.logMessageReceived -= OnLogMessage;

            // Final flush
            FlushExceptions();
        }

        /// <summary>
        /// Enqueues the repeat counts collected so far (e.g. before going to background).
        /// </summary>
        public void FlushPending()
        {
            FlushExceptions();
            _lastCleanupTime = Time.realtimeSinceStartup;
        }

        /// <summary>
        /// Records a breadcrumb for crash context. Does not allocate.
        /// </summary>
        public void RecordBreadcrumb(string message)
        {
            if (string.IsNullOrEmpty(message))
                return;

            int i = (_breadcrumbStart + _breadcrumbCount) % MaxBreadcrumbs;
            if (_breadcrumbCount == MaxBreadcrumbs)
                _breadcrumbStart = (_breadcrumbStart + 1) % MaxBreadcrumbs;
            else
                _breadcrumbCount++;
            _breadcrumbTimes[i] = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            _breadcrumbMessages[i] = message;
        }

        /// <summary>
        /// Gets current breadcrumbs for crash reports, oldest first, as
        /// "[HH:mm:ss.fff] message" (UTC).
        /// </summary>
        public List<string> GetBreadcrumbs()
        {
            return SnapshotBreadcrumbs(MaxBreadcrumbs, int.MaxValue);
        }

        /// <summary>The most recent breadcrumbs (at most max), oldest first, each cut to maxLength.</summary>
        internal List<string> SnapshotBreadcrumbs(int max, int maxLength)
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

        // Internal for tests: the log callback's work.
        internal void HandleLogMessage(string message, string stackTrace, LogType type)
        {
            if (!_isActive)
                return;

            // Only capture errors and exceptions
            if (type != LogType.Error && type != LogType.Exception && type != LogType.Assert)
                return;

            // Never capture the SDK's own output.
            if (message != null && message.StartsWith(ApmClient.LogPrefix, StringComparison.Ordinal))
                return;

            message = message ?? "";
            stackTrace = stackTrace ?? "";
            long nowMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

            // A repeat of a text already seen in this window: count it. No
            // parsing, no allocation.
            var key = new RawKey
            {
                MessageHash = message.GetHashCode(),
                StackHash = stackTrace.GetHashCode(),
                MessageLength = message.Length,
                StackLength = stackTrace.Length,
                Type = type
            };
            if (_fingerprintByRawKey.TryGetValue(key, out var knownFingerprint) &&
                _recentExceptions.TryGetValue(knownFingerprint, out var known))
            {
                CountRepeat(known, nowMs);
                return;
            }

            // First time this text is seen in the window: parse it (allocates).
            var info = Describe(message, stackTrace, type);
            if (_fingerprintByRawKey.Count < MaxRawKeys)
                _fingerprintByRawKey[key] = info.Fingerprint;

            if (_recentExceptions.TryGetValue(info.Fingerprint, out var existing))
            {
                // Same group, different text (e.g. another roomId in the message).
                CountRepeat(existing, nowMs);
                return;
            }

            // New exception
            if (_recentExceptions.Count >= MaxRecentExceptions)
            {
                FlushExceptions();
            }
            _recentExceptions[info.Fingerprint] = info;

            // First occurrence goes to disk right away, with the time it happened
            // and the breadcrumbs that led to it.
            Enqueue(info, 1, nowMs, null, SnapshotBreadcrumbs(MaxBreadcrumbsPerEvent, MaxBreadcrumbLength));

            // Record as breadcrumb (once per group and window, not per repeat)
            RecordBreadcrumb("Exception: " + Truncate(FirstLine(message), 100));
        }

        private static void CountRepeat(ExceptionInfo info, long nowMs)
        {
            // Deduplicate - count it, report at the end of the window
            if (info.RepeatCount == 0)
            {
                info.RepeatFirstSeenMs = nowMs;
                info.RepeatContext = GameContext.Current;
            }
            info.RepeatCount++;
        }

        /// <summary>
        /// Builds what is sent for a log and its fingerprint. An error or
        /// assert whose message embeds an exception (OZX's GameEventBus logs
        /// "[GameEventBus] Error handling event X: {ex}") gets that exception
        /// extracted from the full message first; the fingerprint is computed
        /// from exactly the fields that are sent, as the server will.
        /// </summary>
        private static ExceptionInfo Describe(string message, string stackTrace, LogType type)
        {
            string logType = type == LogType.Exception ? ExceptionParser.LogTypeException
                : type == LogType.Assert ? ExceptionParser.LogTypeAssert
                : ExceptionParser.LogTypeError;
            string embedded = "";
            if (type != LogType.Exception)
            {
                int i = ExceptionParser.FindEmbedded(message);
                if (i >= 0)
                    embedded = ExceptionParser.Compact(message.Substring(i), MaxEmbeddedLength);
            }
            var info = new ExceptionInfo
            {
                LogType = logType,
                Message = TruncateMessage(message, MaxMessageLength),
                Stack = TruncateStack(stackTrace, MaxStackLength),
                Embedded = embedded
            };
            info.Fingerprint = ExceptionParser.Analyze(logType, info.Message, info.Stack, embedded).Fingerprint;
            return info;
        }

        private void FlushExceptions()
        {
            foreach (var kvp in _recentExceptions)
            {
                var info = kvp.Value;
                if (info.RepeatCount > 0)
                    Enqueue(info, info.RepeatCount, info.RepeatFirstSeenMs, info.RepeatContext, null);
            }

            _recentExceptions.Clear();
            _fingerprintByRawKey.Clear();
        }

        private void Enqueue(ExceptionInfo info, int count, long timestampMs, ContextSnapshot context, List<string> breadcrumbs)
        {
            // Repeat counts carry the same text as the first occurrence, so the
            // server computes the same fingerprint for them.
            var evt = new ExceptionEvent
            {
                timestamp = timestampMs,
                fingerprint = info.Fingerprint,
                log_type = info.LogType,
                message = info.Message,
                stack = info.Stack,
                embedded_exception = info.Embedded,
                count = count,
                breadcrumbs = breadcrumbs,
                // null: the context at enqueue time (set by EnqueueEvent)
                ctx = context
            };

            if (EnqueueForTests != null)
                EnqueueForTests(evt);
            else
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
