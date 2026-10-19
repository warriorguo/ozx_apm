using System;
using System.Diagnostics;
using OzxApm.Models;

namespace OzxApm.Core
{
    /// <summary>
    /// One reading of the memory figures (APM-9), in MB; 0 = not available
    /// on this platform or not read. Managed: the managed heap in use (the
    /// perf sample's mem_mb, GC.GetTotalMemory); MonoHeap: the managed heap
    /// the runtime reserved; Alloc / Reserved: Unity's native memory
    /// allocated / reserved (Profiler.GetTotalAllocatedMemoryLong /
    /// GetTotalReservedMemoryLong); Gfx: graphics memory Unity estimates (Gfx
    /// Used Memory); Rss: the process's resident memory (Android: RSS from
    /// /proc/self/statm; elsewhere Unity's System Used Memory).
    /// </summary>
    internal struct MemoryFigures
    {
        public float Managed;
        public float MonoHeap;
        public float Alloc;
        public float Reserved;
        public float Gfx;
        public float Rss;

        internal void MaxWith(in MemoryFigures o)
        {
            if (o.Managed > Managed) Managed = o.Managed;
            if (o.MonoHeap > MonoHeap) MonoHeap = o.MonoHeap;
            if (o.Alloc > Alloc) Alloc = o.Alloc;
            if (o.Reserved > Reserved) Reserved = o.Reserved;
            if (o.Gfx > Gfx) Gfx = o.Gfx;
            if (o.Rss > Rss) Rss = o.Rss;
        }
    }

    /// <summary>What the crash journal records about memory and the run (copied under the tracker's lock).</summary>
    internal struct MemoryJournalState
    {
        /// <summary>The last reading of each figure and when (Unix ms; 0: none yet).</summary>
        public MemoryFigures Last;
        public long LastMs;
        /// <summary>The process's highest RSS so far (MB).</summary>
        public float ProcessPeakRss;
        /// <summary>The open run (null: none), its start (Unix ms), samples, peaks and figures at its start.</summary>
        public string RunId;
        public long RunStartMs;
        public int RunSamples;
        public MemoryFigures RunPeak;
        public float RunStartAlloc;
        public float RunStartRss;
    }

    /// <summary>
    /// The current game run (ApmClient.BeginRun / EndRun, APM-9) and the
    /// memory readings: the latest of each figure, the process's peak RSS and
    /// the open run's peaks. Fed by the perf sample on the main thread (every
    /// SamplingIntervalSeconds) and the RSS reader's thread; read by the
    /// crash journal's thread and BeginRun/EndRun (any thread). One small lock,
    /// taken about twice a second: no allocation, no contention to speak of.
    /// </summary>
    internal sealed class RunTracker
    {
        /// <summary>The context key BeginRun sets to the run id (every event of the run carries it).</summary>
        internal const string ContextKey = "runId";
        internal const int MaxRunIdLength = 64;
        /// <summary>The journal is rewritten when RSS or native memory moved this much since the last rewrite (MB).</summary>
        internal const float JournalStepMb = 32f;

        private readonly object _lock = new object();
        private MemoryJournalState _s;
        private long _runStartTicks;
        private float _notifiedRss, _notifiedAlloc;

        /// <summary>Runs ended in this process (stats).</summary>
        internal int RunsEnded;

        internal string CurrentRunId
        {
            get
            {
                lock (_lock)
                {
                    return _s.RunId;
                }
            }
        }

        /// <summary>
        /// A run id: 1-64 printable characters (no control characters). Game
        /// text: never put personal data in it.
        /// </summary>
        internal static bool IsValidRunId(string id)
        {
            if (string.IsNullOrEmpty(id) || id.Length > MaxRunIdLength)
                return false;
            for (int i = 0; i < id.Length; i++)
            {
                if (char.IsControl(id[i]))
                    return false;
            }
            return true;
        }

        private static long NowMs() => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

        /// <summary>
        /// A reading from the perf sample (main thread): every figure but RSS
        /// where the RSS reader runs (rssFromHere false). Updates the last
        /// readings, the process and run peaks; true when the journal should
        /// be rewritten (memory moved by JournalStepMb).
        /// </summary>
        internal bool Record(in MemoryFigures f, bool rssFromHere)
        {
            lock (_lock)
            {
                var keepRss = _s.Last.Rss;
                _s.Last = f;
                if (!rssFromHere)
                    _s.Last.Rss = keepRss;
                _s.LastMs = NowMs();
                if (_s.Last.Rss > _s.ProcessPeakRss)
                    _s.ProcessPeakRss = _s.Last.Rss;
                if (_s.RunId != null)
                {
                    _s.RunSamples++;
                    _s.RunPeak.MaxWith(_s.Last);
                    if (_s.RunStartAlloc <= 0)
                        _s.RunStartAlloc = _s.Last.Alloc;
                    if (_s.RunStartRss <= 0)
                        _s.RunStartRss = _s.Last.Rss;
                }
                return MovedLocked();
            }
        }

        /// <summary>An RSS reading of the reader thread (MB). True when the journal should be rewritten.</summary>
        internal bool RecordRss(float rssMb)
        {
            lock (_lock)
            {
                _s.Last.Rss = rssMb;
                if (_s.LastMs == 0)
                    _s.LastMs = NowMs();
                if (rssMb > _s.ProcessPeakRss)
                    _s.ProcessPeakRss = rssMb;
                if (_s.RunId != null)
                {
                    if (rssMb > _s.RunPeak.Rss)
                        _s.RunPeak.Rss = rssMb;
                    if (_s.RunStartRss <= 0)
                        _s.RunStartRss = rssMb;
                }
                return MovedLocked();
            }
        }

        /// <summary>The RSS reader failed: the last reading is no longer current (peaks are kept).</summary>
        internal void ClearRss()
        {
            lock (_lock)
            {
                _s.Last.Rss = 0;
            }
        }

        private bool MovedLocked()
        {
            if (Math.Abs(_s.Last.Rss - _notifiedRss) < JournalStepMb && Math.Abs(_s.Last.Alloc - _notifiedAlloc) < JournalStepMb)
                return false;
            _notifiedRss = _s.Last.Rss;
            _notifiedAlloc = _s.Last.Alloc;
            return true;
        }

        /// <summary>
        /// Starts a run. A run still open is ended first and returned (its
        /// event: end reason new_run). The run's peaks start from the latest
        /// reading.
        /// </summary>
        internal RunEvent Begin(string runId)
        {
            lock (_lock)
            {
                RunEvent previous = _s.RunId != null ? EndLocked(RunEvent.EndNewRun, null) : null;
                _s.RunId = runId;
                _s.RunStartMs = NowMs();
                _runStartTicks = Stopwatch.GetTimestamp();
                _s.RunSamples = 0;
                _s.RunPeak = _s.Last;
                _s.RunStartAlloc = _s.Last.Alloc;
                _s.RunStartRss = _s.Last.Rss;
                return previous;
            }
        }

        /// <summary>Ends the open run: its event, or null when none is open.</summary>
        internal RunEvent End(string reason, string result)
        {
            lock (_lock)
            {
                return _s.RunId != null ? EndLocked(reason, result) : null;
            }
        }

        private RunEvent EndLocked(string reason, string result)
        {
            long ms = (long)((Stopwatch.GetTimestamp() - _runStartTicks) * 1000.0 / Stopwatch.Frequency);
            var evt = new RunEvent
            {
                run_id = _s.RunId,
                end_reason = reason,
                result = result,
                duration_ms = Math.Max(0, ms),
                samples = _s.RunSamples,
                peak_mem_mb = _s.RunPeak.Managed,
                peak_mono_heap_mb = _s.RunPeak.MonoHeap,
                peak_alloc_mb = _s.RunPeak.Alloc,
                peak_reserved_mb = _s.RunPeak.Reserved,
                peak_gfx_mb = _s.RunPeak.Gfx,
                peak_rss_mb = _s.RunPeak.Rss,
                start_alloc_mb = _s.RunStartAlloc,
                start_rss_mb = _s.RunStartRss,
                end_alloc_mb = _s.Last.Alloc,
                end_rss_mb = _s.Last.Rss,
                run_start_ms = _s.RunStartMs,
            };
            _s.RunId = null;
            _s.RunStartMs = 0;
            _s.RunSamples = 0;
            _s.RunPeak = default;
            _s.RunStartAlloc = 0;
            _s.RunStartRss = 0;
            RunsEnded++;
            return evt;
        }

        /// <summary>A copy of the state for the crash journal (no allocation).</summary>
        internal void Read(out MemoryJournalState state)
        {
            lock (_lock)
            {
                state = _s;
            }
        }

        internal void ResetForTests()
        {
            lock (_lock)
            {
                _s = default;
                _notifiedRss = _notifiedAlloc = 0;
                RunsEnded = 0;
            }
        }
    }
}
