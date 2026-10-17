using System;
using System.Collections.Generic;
using System.Threading;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Custom events and metrics (APM-6): ApmClient.TrackEvent, RecordMetric
    /// and the timers.
    ///
    /// Events: every call goes onto the time line (Timeline: the breadcrumbs
    /// later exceptions and janks carry). The first MaxEventsPerWindow calls
    /// of a window (ApmConfig.MaxCustomEventsPerMinute) are each sent as an
    /// event with their properties; further calls are counted per (name,
    /// game context) and sent as one event per count at the end of the window
    /// ("aggregated": properties not kept). So sum(count) per name is exact.
    ///
    /// Metrics: samples are aggregated per (name, game context) and window:
    /// count, sum, min, max and a histogram of MetricBuckets keys (2% relative
    /// accuracy) from which the server computes p50/p95/p99. One event per
    /// series and window, whatever the number of samples.
    ///
    /// Window: WindowMs, also ended by FlushPending (pause, Flush, Stop) and
    /// by a new session. Limits per window: MaxSeriesPerName contexts per name
    /// and MaxSeriesPerWindow in all (further contexts of a name go to its
    /// overflow series, whose context is what its samples shared, ctx_merged),
    /// MaxNamesPerWindow names per kind (further names are dropped and
    /// counted in ApmStats).
    ///
    /// Cost: recording a sample is a lock, a dictionary lookup by name and a
    /// binary search in the series' histogram; it allocates nothing once the
    /// name has been seen (series and histograms are reused across windows).
    /// A tracked event allocates the event (and, with properties, one array).
    /// Thread-safe: any thread may record; the window is closed by the main
    /// thread's Update. Nothing is logged or enqueued while the lock is held.
    /// </summary>
    public class CustomCollector : ICollector
    {
        internal const int WindowMs = 60000;
        internal const int MaxSeriesPerWindow = 128;
        internal const int MaxSeriesPerName = 16;
        internal const int MaxNamesPerWindow = 64;
        internal const int HistogramCapacity = 128;
        // Names remembered across windows (so a name seen before costs no
        // allocation); beyond this the table is reset at the end of a window.
        private const int MaxRememberedNames = 4 * MaxNamesPerWindow;

        // Properties: the same limits as the game context (GameContext).
        public const int MaxProps = GameContext.MaxKeys;
        public const int MaxPropValueLength = GameContext.MaxValueLength;
        public const int MaxPropsLength = GameContext.MaxTotalLength;
        public const int MaxUnitLength = 16;

        private const int MaxLoggedViolations = 10;

        private readonly ApmConfig _config;
        private readonly ApmClient _client;
        private readonly Timeline _timeline;
        private SessionClock _clock;
        private volatile bool _isActive;

        private readonly object _sync = new object();

        // ---- Window state, guarded by _sync ----
        private int _windowStartTick;
        private long _windowStartMs;
        private int _windowId;
        private int _windowGeneration = int.MinValue;
        private string _windowSessionId;
        private int _eventsSent;
        private int _seriesUsed;
        private int _eventNamesUsed;
        private int _metricNamesUsed;
        private readonly Dictionary<string, NameEntry> _eventNames = new Dictionary<string, NameEntry>(StringComparer.Ordinal);
        private readonly Dictionary<string, NameEntry> _metricNames = new Dictionary<string, NameEntry>(StringComparer.Ordinal);
        private readonly List<NameEntry> _used = new List<NameEntry>(2 * MaxNamesPerWindow);
        private readonly Stack<Series> _free = new Stack<Series>();

        // ---- Totals since start (ApmStats), guarded by _sync ----
        private long _eventsTracked;
        private long _eventsAggregated;
        private long _eventsDropped;
        private long _samples;
        private long _samplesDropped;
        private static int s_rejected;
        private static int s_loggedViolations;

        private sealed class Series
        {
            public bool IsMetric;
            public string Name;
            public string Unit;
            public ContextSnapshot Ctx;
            public bool Merged;
            public long Count;
            public double Sum;
            public double Min;
            public double Max;
            public long FirstMs;
            public MetricHistogram Hist; // metrics; kept when the series is reused
        }

        private sealed class NameEntry
        {
            public readonly string Name;
            public readonly bool IsMetric;
            public readonly Series[] Series = new Series[MaxSeriesPerName];
            public int Count;
            public Series Overflow;
            public Series Last;
            public int WindowId = -1;

            public NameEntry(string name, bool isMetric)
            {
                Name = name;
                IsMetric = isMetric;
            }
        }

        public bool IsActive => _isActive;

        // Test seams.
        internal Action<BaseEvent> EnqueueForTests;
        internal SessionClock ClockForTests { set => _clock = value; }
        internal void ActivateForTests()
        {
            StartWindow();
            _isActive = true;
        }
        internal void ExpireWindowForTests() => Volatile.Write(ref _windowStartTick, Environment.TickCount - WindowMs - 1);

        internal CustomCollector(ApmConfig config, ApmClient client, Timeline timeline)
        {
            _config = config ?? new ApmConfig();
            _client = client;
            _timeline = timeline;
            _clock = client != null && client.Session != null ? client.Session.Clock : null;
        }

        private int MaxEventsPerWindow => Math.Max(0, _config.MaxCustomEventsPerMinute);

        public void Start()
        {
            StartWindow();
            _isActive = true;
        }

        private void StartWindow()
        {
            lock (_sync)
            {
                _windowStartMs = Timeline.NowMs();
                Volatile.Write(ref _windowStartTick, Environment.TickCount);
                SyncSessionLocked();
            }
        }

        public void Update()
        {
            if (!_isActive)
                return;
            var clock = _clock;
            if (unchecked(Environment.TickCount - Volatile.Read(ref _windowStartTick)) >= WindowMs ||
                (clock != null && clock.Generation != Volatile.Read(ref _windowGeneration)))
                FlushPending();
        }

        public void Stop()
        {
            _isActive = false;
            FlushPending();
        }

        /// <summary>Ends the window: enqueues its counts and metric series. Any thread.</summary>
        public void FlushPending()
        {
            List<BaseEvent> events;
            lock (_sync)
            {
                events = CloseWindowLocked();
                SyncSessionLocked();
            }
            EnqueueAll(events);
        }

        internal void GetStats(out long eventsTracked, out long eventsAggregated, out long eventsDropped,
            out long samples, out long samplesDropped)
        {
            lock (_sync)
            {
                eventsTracked = _eventsTracked;
                eventsAggregated = _eventsAggregated;
                eventsDropped = _eventsDropped;
                samples = _samples;
                samplesDropped = _samplesDropped;
            }
        }

        /// <summary>Invalid names, values and properties refused or dropped since start (all instances).</summary>
        internal static int RejectedCount => Volatile.Read(ref s_rejected);

        internal static void ResetStaticsForTests()
        {
            s_rejected = 0;
            s_loggedViolations = 0;
        }

        // ------------------------------------------------------------------
        // Events
        // ------------------------------------------------------------------

        /// <summary>
        /// Records a custom event. props: validated key, value pairs (see
        /// ValidateProps), kept as is; null for none. False if not collecting
        /// or the name is invalid.
        /// </summary>
        internal bool TrackEvent(string name, string[] props)
        {
            if (!_isActive)
                return false;
            if (!GameContext.IsValidKey(name))
            {
                Violation("TrackEvent", name, "invalid name (1-64 characters from [A-Za-z0-9_.-])");
                return false;
            }
            long now = Timeline.NowMs();
            var ctx = GameContext.Current;
            _timeline?.RecordEvent(name, props, now);

            CustomEvent evt = null;
            List<BaseEvent> closed;
            lock (_sync)
            {
                if (!_isActive)
                    return false;
                closed = SyncSessionLocked();
                _eventsTracked++;
                if (_eventsSent < MaxEventsPerWindow)
                {
                    _eventsSent++;
                    evt = new CustomEvent { timestamp = now, name = name, props = props, ctx = ctx };
                }
                else
                {
                    var s = FindSeriesLocked(false, name, ctx, now);
                    if (s == null)
                    {
                        _eventsDropped++;
                    }
                    else
                    {
                        if (s.Count == 0)
                            s.FirstMs = now;
                        s.Count++;
                        _eventsAggregated++;
                    }
                }
            }
            EnqueueAll(closed);
            if (evt != null)
                Enqueue(evt);
            return true;
        }

        /// <summary>
        /// Validates properties in place: pairs[0..length) as key, value. Keys
        /// must be valid context keys and unique (the first wins); null or empty
        /// values are left out; values are cut to MaxPropValueLength; at most
        /// MaxProps entries and MaxPropsLength characters (key + value + 6 per
        /// entry). Returns the pairs kept (pairs itself if nothing was left out,
        /// a shorter copy otherwise, null if none).
        /// </summary>
        internal static string[] ValidateProps(string[] pairs, int length)
        {
            if (pairs == null || length < 2)
                return null;
            int w = 0, total = 0;
            for (int r = 0; r + 1 < length; r += 2)
            {
                string k = pairs[r], v = pairs[r + 1];
                if (!GameContext.IsValidKey(k))
                {
                    Violation("TrackEvent", k, "invalid property key (1-64 characters from [A-Za-z0-9_.-]); left out");
                    continue;
                }
                if (string.IsNullOrEmpty(v))
                    continue;
                bool duplicate = false;
                for (int j = 0; j < w; j += 2)
                {
                    if (string.Equals(pairs[j], k, StringComparison.Ordinal))
                    {
                        duplicate = true;
                        break;
                    }
                }
                if (duplicate)
                    continue;
                if (w / 2 >= MaxProps)
                {
                    Violation("TrackEvent", k, "more than 64 properties; left out");
                    continue;
                }
                if (v.Length > MaxPropValueLength)
                {
                    int n = MaxPropValueLength;
                    if (char.IsHighSurrogate(v[n - 1]))
                        n--;
                    v = v.Substring(0, n);
                    Violation("TrackEvent", k, "value cut to 256 characters");
                }
                int entry = k.Length + v.Length + 6;
                if (total + entry > MaxPropsLength)
                {
                    Violation("TrackEvent", k, "properties larger than 4096 characters; left out");
                    continue;
                }
                total += entry;
                pairs[w++] = k;
                pairs[w++] = v;
            }
            if (w == 0)
                return null;
            if (w == pairs.Length)
                return pairs;
            var kept = new string[w];
            Array.Copy(pairs, kept, w);
            return kept;
        }

        // ------------------------------------------------------------------
        // Metrics
        // ------------------------------------------------------------------

        /// <summary>
        /// Adds a sample to its series. False if not collecting, or the name or
        /// value is invalid. No allocation once the name was seen.
        /// </summary>
        internal bool RecordMetric(string name, double value, string unit)
        {
            if (!_isActive)
                return false;
            if (double.IsNaN(value) || double.IsInfinity(value))
            {
                Violation("RecordMetric", name, "value is NaN or infinite");
                return false;
            }
            if (!GameContext.IsValidKey(name))
            {
                Violation("RecordMetric", name, "invalid name (1-64 characters from [A-Za-z0-9_.-])");
                return false;
            }
            var ctx = GameContext.Current;
            int key = MetricBuckets.Key(value);
            List<BaseEvent> closed;
            lock (_sync)
            {
                if (!_isActive)
                    return false;
                closed = SyncSessionLocked();
                var s = FindSeriesLocked(true, name, ctx, 0);
                if (s == null)
                {
                    _samplesDropped++;
                }
                else
                {
                    if (s.Count == 0)
                    {
                        s.FirstMs = Timeline.NowMs();
                        s.Unit = ValidUnit(unit) ? unit : "";
                        s.Min = value;
                        s.Max = value;
                    }
                    else
                    {
                        if (value < s.Min)
                            s.Min = value;
                        if (value > s.Max)
                            s.Max = value;
                    }
                    s.Count++;
                    s.Sum += value;
                    s.Hist.Add(key);
                    _samples++;
                }
            }
            EnqueueAll(closed);
            return true;
        }

        private static bool ValidUnit(string unit)
        {
            if (string.IsNullOrEmpty(unit) || unit.Length > MaxUnitLength)
                return false;
            for (int i = 0; i < unit.Length; i++)
            {
                char c = unit[i];
                if (c < 0x20 || c > 0x7e || c == '"' || c == '\\')
                    return false;
            }
            return true;
        }

        // ------------------------------------------------------------------
        // Series (caller holds _sync)
        // ------------------------------------------------------------------

        private Series FindSeriesLocked(bool metric, string name, ContextSnapshot ctx, long nowMs)
        {
            var names = metric ? _metricNames : _eventNames;
            if (!names.TryGetValue(name, out var ne))
            {
                if ((metric ? _metricNamesUsed : _eventNamesUsed) >= MaxNamesPerWindow)
                    return null;
                ne = new NameEntry(name, metric);
                names[name] = ne;
            }
            if (ne.WindowId != _windowId)
            {
                if ((metric ? _metricNamesUsed : _eventNamesUsed) >= MaxNamesPerWindow)
                    return null;
                if (metric)
                    _metricNamesUsed++;
                else
                    _eventNamesUsed++;
                ne.WindowId = _windowId;
                ne.Count = 0;
                ne.Overflow = null;
                ne.Last = null;
                _used.Add(ne);
            }

            var last = ne.Last;
            if (last != null && last != ne.Overflow && ReferenceEquals(last.Ctx, ctx))
                return last;
            for (int i = 0; i < ne.Count; i++)
            {
                var s = ne.Series[i];
                if (s.Ctx.SameAs(ctx))
                {
                    ne.Last = s;
                    return s;
                }
            }

            Series found;
            if (ne.Count < MaxSeriesPerName && _seriesUsed < MaxSeriesPerWindow)
            {
                found = Rent(metric, ne.Name, ctx);
                ne.Series[ne.Count++] = found;
                _seriesUsed++;
            }
            else if (ne.Overflow == null)
            {
                // More contexts than are kept apart: one series per name keeping
                // only the context entries its samples share.
                found = Rent(metric, ne.Name, ctx);
                ne.Overflow = found;
            }
            else
            {
                found = ne.Overflow;
                if (!found.Ctx.SameAs(ctx))
                {
                    found.Ctx = found.Ctx.Intersect(ctx);
                    found.Merged = true;
                }
            }
            ne.Last = found;
            return found;
        }

        private Series Rent(bool metric, string name, ContextSnapshot ctx)
        {
            var s = _free.Count > 0 ? _free.Pop() : new Series();
            s.IsMetric = metric;
            s.Name = name;
            s.Unit = "";
            s.Ctx = ctx ?? ContextSnapshot.Empty;
            s.Merged = false;
            s.Count = 0;
            s.Sum = 0;
            s.Min = 0;
            s.Max = 0;
            s.FirstMs = 0;
            if (metric)
            {
                if (s.Hist == null)
                    s.Hist = new MetricHistogram(HistogramCapacity);
                else
                    s.Hist.Clear();
            }
            return s;
        }

        private void Return(Series s)
        {
            s.Name = null;
            s.Ctx = null;
            s.Unit = null;
            _free.Push(s);
        }

        /// <summary>
        /// If a new session started since the window opened, closes the window
        /// (its events keep the old session's id) and returns its events, to be
        /// enqueued after the lock is released; null otherwise. Caller holds _sync.
        /// </summary>
        private List<BaseEvent> SyncSessionLocked()
        {
            var clock = _clock;
            if (clock == null)
                return null;
            int generation = clock.Generation;
            if (generation == _windowGeneration)
                return null;
            var events = _windowGeneration == int.MinValue ? null : CloseWindowLocked();
            _windowGeneration = generation;
            _windowSessionId = clock.SessionId;
            return events;
        }

        /// <summary>
        /// The window's events and a new window. Caller holds _sync. Null,
        /// without allocating, when there is nothing to send.
        /// </summary>
        private List<BaseEvent> CloseWindowLocked()
        {
            List<BaseEvent> events = null;
            long now = Timeline.NowMs();
            if (_used.Count > 0)
            {
                long windowMs = Math.Max(0, now - _windowStartMs);
                events = new List<BaseEvent>(_seriesUsed + _used.Count);
                for (int n = 0; n < _used.Count; n++)
                {
                    var ne = _used[n];
                    for (int i = 0; i < ne.Count; i++)
                    {
                        AddEvent(events, ne.Series[i], windowMs);
                        Return(ne.Series[i]);
                        ne.Series[i] = null;
                    }
                    if (ne.Overflow != null)
                    {
                        AddEvent(events, ne.Overflow, windowMs);
                        Return(ne.Overflow);
                    }
                    ne.Count = 0;
                    ne.Overflow = null;
                    ne.Last = null;
                    ne.WindowId = -1;
                }
                _used.Clear();
                if (_eventNames.Count > MaxRememberedNames)
                    _eventNames.Clear();
                if (_metricNames.Count > MaxRememberedNames)
                    _metricNames.Clear();
            }
            _eventsSent = 0;
            _seriesUsed = 0;
            _eventNamesUsed = 0;
            _metricNamesUsed = 0;
            _windowId++;
            _windowStartMs = now;
            Volatile.Write(ref _windowStartTick, Environment.TickCount);
            return events;
        }

        private void AddEvent(List<BaseEvent> events, Series s, long windowMs)
        {
            if (s.Count <= 0)
                return;
            if (!s.IsMetric)
            {
                events.Add(new CustomEvent
                {
                    timestamp = s.FirstMs,
                    name = s.Name,
                    count = (int)Math.Min(s.Count, int.MaxValue),
                    aggregated = true,
                    ctx_merged = s.Merged,
                    ctx = s.Ctx,
                    session_id = _windowSessionId
                });
                return;
            }
            s.Hist.CopyTo(out int[] keys, out long[] counts);
            events.Add(new CustomMetricEvent
            {
                timestamp = s.FirstMs,
                name = s.Name,
                unit = s.Unit,
                count = s.Count,
                sum = s.Sum,
                min = s.Min,
                max = s.Max,
                window_ms = windowMs,
                hist_v = MetricBuckets.Version,
                hist_keys = keys,
                hist_counts = counts,
                ctx_merged = s.Merged,
                ctx = s.Ctx,
                session_id = _windowSessionId
            });
        }

        private void EnqueueAll(List<BaseEvent> events)
        {
            if (events == null)
                return;
            for (int i = 0; i < events.Count; i++)
                Enqueue(events[i]);
        }

        // Never called with _sync held.
        private void Enqueue(BaseEvent evt)
        {
            var test = EnqueueForTests;
            if (test != null)
            {
                test(evt);
                return;
            }
            _client?.EnqueueEvent(evt);
        }

        private static void Violation(string api, string key, string what)
        {
            Interlocked.Increment(ref s_rejected);
            // A warning, never an error (see GameContext). Only the first few.
            if (Interlocked.Increment(ref s_loggedViolations) > MaxLoggedViolations)
                return;
            string shown = key == null ? "null" : key.Length > GameContext.MaxKeyLength ? key.Substring(0, GameContext.MaxKeyLength) + "..." : key;
            ApmClient.Log(LogLevel.Warning, api + "(\"" + shown + "\"): " + what);
        }
    }
}
