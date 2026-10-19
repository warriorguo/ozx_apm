using System;
using System.Threading;
using Unity.Profiling;
using UnityEngine.Profiling;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Collectors
{
    /// <summary>Reads the memory figures the engine gives (main thread). Tests replace it.</summary>
    internal interface IMemorySource : IDisposable
    {
        /// <summary>Fills MonoHeap, Alloc, Reserved, Gfx and, where withRss, Rss (MB; 0: not available).</summary>
        void Read(ref MemoryFigures f, bool withRss);
    }

    /// <summary>
    /// Unity's memory figures (APM-9): Profiler.GetMonoHeapSizeLong,
    /// GetTotalAllocatedMemoryLong, GetTotalReservedMemoryLong (engine calls,
    /// no allocation) and two profiler counters read through ProfilerRecorder
    /// (created once; reading one is an engine call, no allocation): "Gfx Used
    /// Memory" and, where the process RSS is not read from /proc,
    /// "System Used Memory". A figure whose call throws (not in this player or
    /// outside Unity) is not read again.
    /// </summary>
    internal sealed class UnityMemorySource : IMemorySource
    {
        private const float Mb = 1024f * 1024f;
        private ProfilerRecorder _gfx;
        private ProfilerRecorder _system;
        private bool _profilerFailed;
        private bool _recordersFailed;

        internal UnityMemorySource(bool withSystem)
        {
            try
            {
                _gfx = ProfilerRecorder.StartNew(ProfilerCategory.Memory, "Gfx Used Memory");
                if (withSystem)
                    _system = ProfilerRecorder.StartNew(ProfilerCategory.Memory, "System Used Memory");
            }
            catch (Exception ex)
            {
                _recordersFailed = true;
                ApmClient.ReportInternalError("UnityMemorySource", ex);
            }
        }

        public void Read(ref MemoryFigures f, bool withRss)
        {
            if (!_profilerFailed)
            {
                try
                {
                    f.MonoHeap = Profiler.GetMonoHeapSizeLong() / Mb;
                    f.Alloc = Profiler.GetTotalAllocatedMemoryLong() / Mb;
                    f.Reserved = Profiler.GetTotalReservedMemoryLong() / Mb;
                }
                catch (Exception ex)
                {
                    _profilerFailed = true;
                    ApmClient.ReportInternalError("Profiler memory", ex);
                }
            }
            if (!_recordersFailed)
            {
                try
                {
                    if (_gfx.Valid)
                        f.Gfx = _gfx.LastValue / Mb;
                    if (withRss && _system.Valid)
                        f.Rss = _system.LastValue / Mb;
                }
                catch (Exception ex)
                {
                    _recordersFailed = true;
                    ApmClient.ReportInternalError("ProfilerRecorder memory", ex);
                }
            }
        }

        public void Dispose()
        {
            try
            {
                if (_gfx.Valid)
                    _gfx.Dispose();
                if (_system.Valid)
                    _system.Dispose();
            }
            catch
            {
                // Disposing never throws into the game.
            }
        }
    }

    /// <summary>
    /// The memory breakdown of the perf sample and the memory of game runs
    /// (APM-9). On the main thread, once per perf sample (every
    /// SamplingIntervalSeconds, not per frame): Unity's figures
    /// (IMemorySource) into the sample and the RunTracker. On Android the
    /// process's RSS is read from /proc/self/statm by a thread of its own
    /// ("OzxApm.Memory", at the same interval, foreground and background):
    /// never on the main thread. Elsewhere the RSS figure is Unity's System
    /// Used Memory, read with the other figures.
    ///
    /// When memory moved by RunTracker.JournalStepMb the crash journal is
    /// rewritten (CrashJournal.NotifyChanged), so an OOM kill's report carries
    /// memory close to the peak, not only the 5 s heartbeat's.
    /// </summary>
    internal sealed class MemorySampler
    {
        private readonly RunTracker _runs;
        private readonly Func<IMemorySource> _sourceFactory;
        private readonly string _statmPath;
        private readonly int _intervalMs;
        private IMemorySource _source;
        private Thread _thread;
        private volatile bool _stop;
        private readonly AutoResetEvent _wake = new AutoResetEvent(false);
        private ProcStatm _statm;

        /// <summary>Main-thread time of the last Sample, in Stopwatch ticks (stats).</summary>
        internal long LastSampleTicks;
        /// <summary>Duration of the last RSS reading on its thread, in Stopwatch ticks (stats).</summary>
        internal long LastRssReadTicks;
        /// <summary>The last RSS reading (MB), 0 before the first.</summary>
        internal volatile float LastRssMb;
        /// <summary>RSS readings so far (stats, tests).</summary>
        internal long RssReadings;
        /// <summary>
        /// The game is paused (background): the RSS thread only reads every
        /// PausedIntervalMs; on resume it reads at once.
        /// </summary>
        internal bool Paused
        {
            get => _paused;
            set
            {
                bool resumed = _paused && !value;
                _paused = value;
                if (resumed)
                    _wake.Set();
            }
        }

        private volatile bool _paused;
        internal const int PausedIntervalMs = 10000;

        /// <param name="statmPath">/proc/self/statm on Android; null elsewhere (RSS from the engine's counter).</param>
        internal MemorySampler(RunTracker runs, Func<IMemorySource> sourceFactory, string statmPath, int intervalMs)
        {
            _runs = runs;
            _sourceFactory = sourceFactory;
            _statmPath = statmPath;
            _intervalMs = Math.Max(100, intervalMs);
        }

        internal bool ReadsProcRss => _statmPath != null;

        internal bool IsRunning => _thread != null;

        /// <summary>Collecting started (main thread): the engine's recorders and the RSS thread.</summary>
        internal void Start(bool useThread = true)
        {
            if (_source == null)
            {
                try
                {
                    _source = _sourceFactory?.Invoke();
                }
                catch (Exception ex)
                {
                    ApmClient.ReportInternalError("MemorySampler.Start", ex);
                }
            }
            if (_statmPath == null || _thread != null)
                return;
            _stop = false;
            if (_statm == null)
                _statm = new ProcStatm(_statmPath);
            if (!useThread)
                return; // tests call ReadRssNow
            try
            {
                // A small stack: the loop holds a few locals.
                var t = new Thread(Loop, 128 * 1024) { IsBackground = true, Name = "OzxApm.Memory" };
                _thread = t;
                t.Start();
            }
            catch (Exception ex)
            {
                _thread = null;
                ApmClient.ReportInternalError("MemorySampler.Thread", ex);
            }
        }

        /// <summary>Collecting stopped (consent withdrawn, shutdown): the thread ends; the recorders stay until Dispose.</summary>
        internal void Stop()
        {
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
        }

        /// <summary>Shutdown: also the engine's recorders and the open file.</summary>
        internal void Dispose()
        {
            Stop();
            _source?.Dispose();
            _source = null;
            _statm?.Dispose();
            _statm = null;
        }

        private void Loop()
        {
            try
            {
                while (!_stop)
                {
                    ReadRssNow();
                    _wake.WaitOne(_paused ? PausedIntervalMs : _intervalMs);
                }
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("MemorySampler.Loop", ex);
            }
            finally
            {
                _statm?.Dispose();
            }
        }

        /// <summary>One RSS reading (the thread; tests). False when it could not be read.</summary>
        internal bool ReadRssNow()
        {
            var statm = _statm;
            if (statm == null)
                return false;
            long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
            long bytes = statm.ReadRssBytes();
            LastRssReadTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
            if (bytes < 0)
                return false;
            float mb = bytes / (1024f * 1024f);
            LastRssMb = mb;
            Interlocked.Increment(ref RssReadings);
            if (_runs.RecordRss(mb))
                CrashJournal.NotifyChanged();
            return true;
        }

        /// <summary>
        /// The perf sample's memory figures (main thread, once per sample):
        /// managedMb is the managed heap in use (mem_mb). Allocation-free.
        /// </summary>
        internal void Sample(PerfSampleEvent evt, float managedMb)
        {
            long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
            var f = new MemoryFigures { Managed = managedMb };
            bool rssHere = _statmPath == null;
            _source?.Read(ref f, rssHere);
            if (!rssHere)
                f.Rss = LastRssMb;
            evt.mem_mono_heap_mb = f.MonoHeap;
            evt.mem_alloc_mb = f.Alloc;
            evt.mem_reserved_mb = f.Reserved;
            evt.mem_gfx_mb = f.Gfx;
            evt.mem_rss_mb = f.Rss;
            if (_runs.Record(f, rssHere))
                CrashJournal.NotifyChanged();
            LastSampleTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
        }
    }
}
