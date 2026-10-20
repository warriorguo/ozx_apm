using System;
using System.Collections.Generic;
using System.Text;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>
    /// Immutable copy of the game context at one point in time. Every event
    /// keeps a reference to the snapshot that was current when it was enqueued
    /// and is serialized (and so persisted) with it. Its JSON is built once and
    /// shared by all events until the context changes.
    /// </summary>
    public sealed class ContextSnapshot
    {
        /// <summary>The empty context.</summary>
        public static readonly ContextSnapshot Empty = new ContextSnapshot(0, new string[0], new string[0]);

        internal readonly int Version;
        private readonly string[] _keys;   // ordinal order
        private readonly string[] _values;

        /// <summary>The context as a JSON object ({"key":"value",...}); null when empty.</summary>
        internal readonly string Json;

        /// <summary>Json as UTF-8, copied as is into every event's line (APM-3); null when empty.</summary>
        internal readonly byte[] JsonUtf8;

        internal ContextSnapshot(int version, string[] keys, string[] values)
        {
            Version = version;
            _keys = keys;
            _values = values;
            Json = keys.Length == 0 ? null : BuildJson(keys, values);
            JsonUtf8 = Json == null ? null : Encoding.UTF8.GetBytes(Json);
        }

        public int Count => _keys.Length;

        /// <summary>Key at index (ordinal order), 0 &lt;= index &lt; Count.</summary>
        public string GetKey(int index) => _keys[index];

        /// <summary>Value at index, 0 &lt;= index &lt; Count.</summary>
        public string GetValue(int index) => _values[index];

        public bool TryGetValue(string key, out string value)
        {
            int i = key == null ? -1 : Array.BinarySearch(_keys, key, StringComparer.Ordinal);
            value = i >= 0 ? _values[i] : null;
            return i >= 0;
        }

        /// <summary>True if other holds the same keys and values. No allocation.</summary>
        internal bool SameAs(ContextSnapshot other)
        {
            if (ReferenceEquals(this, other))
                return true;
            if (other == null || other._keys.Length != _keys.Length)
                return false;
            // Json lists the keys in ordinal order: equal text, equal context.
            return string.Equals(Json, other.Json, StringComparison.Ordinal);
        }

        /// <summary>
        /// The entries this and other have in common (same key, same value). Returns
        /// this, without allocating, if other has all of them (APM-3: the context
        /// of a repeat count that merges occurrences with different contexts).
        /// </summary>
        internal ContextSnapshot Intersect(ContextSnapshot other)
        {
            if (other == null)
                return Empty;
            if (ReferenceEquals(this, other))
                return this;
            int common = 0;
            for (int i = 0, j = 0; i < _keys.Length && j < other._keys.Length;)
            {
                int c = string.CompareOrdinal(_keys[i], other._keys[j]);
                if (c == 0)
                {
                    if (string.Equals(_values[i], other._values[j], StringComparison.Ordinal))
                        common++;
                    i++;
                    j++;
                }
                else if (c < 0)
                    i++;
                else
                    j++;
            }
            if (common == _keys.Length)
                return this;
            if (common == 0)
                return Empty;
            var keys = new string[common];
            var values = new string[common];
            int n = 0;
            for (int i = 0, j = 0; i < _keys.Length && j < other._keys.Length;)
            {
                int c = string.CompareOrdinal(_keys[i], other._keys[j]);
                if (c == 0)
                {
                    if (string.Equals(_values[i], other._values[j], StringComparison.Ordinal))
                    {
                        keys[n] = _keys[i];
                        values[n] = _values[i];
                        n++;
                    }
                    i++;
                    j++;
                }
                else if (c < 0)
                    i++;
                else
                    j++;
            }
            // Not GameContext's current snapshot: a version it never has.
            return new ContextSnapshot(-1, keys, values);
        }

        /// <summary>
        /// The entries whose key is in keys (APM-12: the ops events' context
        /// whitelist). Returns this, without allocating, if every entry is kept;
        /// Empty if none is.
        /// </summary>
        internal ContextSnapshot Select(ICollection<string> keys)
        {
            if (keys == null)
                return this;
            int kept = 0;
            for (int i = 0; i < _keys.Length; i++)
            {
                if (keys.Contains(_keys[i]))
                    kept++;
            }
            if (kept == _keys.Length)
                return this;
            if (kept == 0)
                return Empty;
            var k = new string[kept];
            var v = new string[kept];
            int n = 0;
            for (int i = 0; i < _keys.Length; i++)
            {
                if (keys.Contains(_keys[i]))
                {
                    k[n] = _keys[i];
                    v[n] = _values[i];
                    n++;
                }
            }
            return new ContextSnapshot(-1, k, v);
        }

        private static string BuildJson(string[] keys, string[] values)
        {
            var sb = new StringBuilder(16 + keys.Length * 32);
            sb.Append('{');
            for (int i = 0; i < keys.Length; i++)
            {
                if (i > 0)
                    sb.Append(',');
                sb.Append('"');
                JsonSerializer.AppendEscapedString(sb, keys[i]);
                sb.Append("\":\"");
                JsonSerializer.AppendEscapedString(sb, values[i]);
                sb.Append('"');
            }
            sb.Append('}');
            return sb.ToString();
        }
    }

    /// <summary>
    /// The game context (APM-5): key/value pairs the game sets with
    /// ApmClient.SetContext (level, room, seed, loadout, data version, ...).
    /// Every event enqueued afterwards carries a snapshot of it, taken at enqueue
    /// time, which is persisted with the event.
    ///
    /// Cost: changing the context marks it dirty (no allocation if the value is
    /// unchanged); the snapshot and its JSON are rebuilt once, by the next event
    /// that is enqueued, and then shared. Reading an unchanged snapshot is two
    /// volatile reads: no lock, no allocation.
    ///
    /// Limits (the server enforces the same): at most MaxKeys keys; keys are 1-64
    /// characters from [A-Za-z0-9_.-]; values are cut to MaxValueLength
    /// characters; the whole context to MaxTotalLength characters. Violations
    /// are counted (ApmStats) and logged as warnings (the first few only); they
    /// never throw.
    ///
    /// Thread-safe: may be changed and read from any thread.
    /// </summary>
    public static class GameContext
    {
        public const int MaxKeys = 64;
        public const int MaxKeyLength = 64;
        public const int MaxValueLength = 256;
        /// <summary>
        /// Upper bound of the context's size: the sum of key and value lengths plus
        /// 6 characters per entry (roughly its JSON size). Every event, including the
        /// once-per-second perf sample, carries the context, so it is bounded more
        /// tightly than MaxKeys x MaxValueLength.
        /// </summary>
        public const int MaxTotalLength = 4096;

        private const int MaxLoggedViolations = 10;

        private static readonly object s_lock = new object();
        private static readonly Dictionary<string, string> s_values = new Dictionary<string, string>(StringComparer.Ordinal);
        private static int s_totalLength;
        private static volatile int s_version;
        private static volatile ContextSnapshot s_snapshot = ContextSnapshot.Empty;
        private static int s_rejected;
        private static int s_truncated;
        private static int s_loggedViolations;

        /// <summary>Keys currently set.</summary>
        public static int Count
        {
            get
            {
                lock (s_lock)
                {
                    return s_values.Count;
                }
            }
        }

        /// <summary>SetContext calls refused so far (invalid key, too many keys, context too large).</summary>
        public static int RejectedCount => s_rejected;

        /// <summary>Changes with every change of the context (APM-7: the crash journal). Any thread.</summary>
        internal static int Version => s_version;

        /// <summary>Values cut to MaxValueLength so far.</summary>
        public static int TruncatedCount => s_truncated;

        /// <summary>
        /// The current snapshot (rebuilt here if the context changed since the last
        /// call). Allocation-free while the context is unchanged.
        /// </summary>
        public static ContextSnapshot Current
        {
            get
            {
                var snapshot = s_snapshot;
                if (snapshot.Version == s_version)
                    return snapshot;
                lock (s_lock)
                {
                    if (s_snapshot.Version != s_version)
                        s_snapshot = Build();
                    return s_snapshot;
                }
            }
        }

        /// <summary>
        /// The count ranges of ApmClient.SetContextCount (APM-9). The server
        /// groups raw numeric context values into the same ranges
        /// (models.CountBucketLabels): both must change together.
        /// </summary>
        internal static readonly string[] CountBucketLabels = { "0", "1-5", "6-10", "11-20", "21-30", "31-50", "51-100", "101-200", "201+" };
        /// <summary>Exclusive upper bounds of CountBucketLabels but the last.</summary>
        internal static readonly long[] CountBucketUpper = { 1, 6, 11, 21, 31, 51, 101, 201 };

        /// <summary>The count range of n (a constant string: no allocation). Negative counts read as 0.</summary>
        internal static string CountBucket(long n)
        {
            for (int i = 0; i < CountBucketUpper.Length; i++)
            {
                if (n < CountBucketUpper[i])
                    return CountBucketLabels[i];
            }
            return CountBucketLabels[CountBucketLabels.Length - 1];
        }

        /// <summary>True if key is 1-64 characters from [A-Za-z0-9_.-].</summary>
        public static bool IsValidKey(string key)
        {
            if (string.IsNullOrEmpty(key) || key.Length > MaxKeyLength)
                return false;
            for (int i = 0; i < key.Length; i++)
            {
                char c = key[i];
                bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
                          c == '_' || c == '.' || c == '-';
                if (!ok)
                    return false;
            }
            return true;
        }

        /// <summary>
        /// Sets key to value. A null or empty value removes the key. A value longer
        /// than MaxValueLength is cut (and counted). Returns false, without
        /// throwing, if the key is invalid or the context would exceed MaxKeys or
        /// MaxTotalLength; in that case a previous value of the key is removed, so
        /// events never carry a stale value.
        /// </summary>
        internal static bool Set(string key, string value)
        {
            if (!IsValidKey(key))
            {
                Violation("invalid key", key, true);
                return false;
            }
            if (string.IsNullOrEmpty(value))
            {
                Remove(key);
                return true;
            }
            // Length after cutting to MaxValueLength, never inside a surrogate pair.
            // The cut copy is only made if the value changes: setting the same
            // over-long value every frame must not allocate either.
            int length = value.Length;
            bool truncated = false;
            if (length > MaxValueLength)
            {
                length = MaxValueLength;
                if (char.IsHighSurrogate(value[length - 1]))
                    length--;
                truncated = true;
            }

            string reason = null;
            int versionBefore;
            lock (s_lock)
            {
                versionBefore = s_version;
                bool exists = s_values.TryGetValue(key, out string old);
                if (exists && old.Length == length && string.CompareOrdinal(old, 0, value, 0, length) == 0)
                {
                    // Unchanged: no new snapshot, no allocation.
                }
                else
                {
                    if (truncated)
                        value = value.Substring(0, length);
                    int oldLength = exists ? EntryLength(key, old) : 0;
                    int newTotal = s_totalLength - oldLength + EntryLength(key, value);
                    if (!exists && s_values.Count >= MaxKeys)
                        reason = "too many keys (max 64)";
                    else if (newTotal > MaxTotalLength)
                        reason = "context too large (max 4096 characters)";

                    if (reason == null)
                    {
                        s_values[key] = value;
                        s_totalLength = newTotal;
                        s_version++;
                    }
                    else if (exists)
                    {
                        s_values.Remove(key);
                        s_totalLength -= oldLength;
                        s_version++;
                    }
                }
            }

            if (s_version != versionBefore)
                CrashJournal.NotifyChanged();
            if (reason != null)
            {
                Violation(reason, key, true);
                return false;
            }
            if (truncated)
                Violation("value cut to 256 characters", key, false);
            return true;
        }

        /// <summary>Removes key. Returns true if it was set.</summary>
        internal static bool Remove(string key)
        {
            if (key == null)
                return false;
            lock (s_lock)
            {
                if (!s_values.TryGetValue(key, out string old))
                    return false;
                s_values.Remove(key);
                s_totalLength -= EntryLength(key, old);
                s_version++;
            }
            CrashJournal.NotifyChanged();
            return true;
        }

        /// <summary>Removes every key.</summary>
        internal static void Clear()
        {
            lock (s_lock)
            {
                if (s_values.Count == 0)
                    return;
                s_values.Clear();
                s_totalLength = 0;
                s_version++;
            }
            CrashJournal.NotifyChanged();
        }

        internal static void ResetForTests()
        {
            lock (s_lock)
            {
                s_values.Clear();
                s_totalLength = 0;
                s_version = 0;
                s_snapshot = ContextSnapshot.Empty;
                s_rejected = 0;
                s_truncated = 0;
                s_loggedViolations = 0;
            }
        }

        private static int EntryLength(string key, string value)
        {
            return key.Length + value.Length + 6; // "k":"v",
        }

        // Caller holds s_lock.
        private static ContextSnapshot Build()
        {
            int n = s_values.Count;
            var keys = new string[n];
            s_values.Keys.CopyTo(keys, 0);
            Array.Sort(keys, StringComparer.Ordinal);
            var values = new string[n];
            for (int i = 0; i < n; i++)
                values[i] = s_values[keys[i]];
            return new ContextSnapshot(s_version, keys, values);
        }

        private static void Violation(string what, string key, bool rejected)
        {
            if (rejected)
                System.Threading.Interlocked.Increment(ref s_rejected);
            else
                System.Threading.Interlocked.Increment(ref s_truncated);
            // A warning, never an error: the SDK must not trigger its own (or the
            // game's) error reporting. Only the first few, in case of a call per frame.
            if (System.Threading.Interlocked.Increment(ref s_loggedViolations) > MaxLoggedViolations)
                return;
            string shown = key == null ? "null" : key.Length > MaxKeyLength ? key.Substring(0, MaxKeyLength) + "..." : key;
            ApmClient.Log(LogLevel.Warning, "SetContext(\"" + shown + "\"): " + what + (rejected ? "; not set" : ""));
        }
    }
}
