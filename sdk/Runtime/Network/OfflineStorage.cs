using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Text;
using System.Threading;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Network
{
    /// <summary>
    /// One upload request read from local storage: one legacy file, or one or
    /// more consecutive segments merged up to BatchSize / MaxBatchBytes.
    /// </summary>
    public sealed class UploadBatch
    {
        /// <summary>Name of the first (oldest) segment in the batch.</summary>
        public string SegmentName;
        /// <summary>Request body, gzip-compressed when Compressed is true.</summary>
        public byte[] Payload;
        public bool Compressed;
        public int EventCount;
        public int UncompressedBytes;
        /// <summary>Uncompressed JSON for the network log; only filled when LogLevel is Debug.</summary>
        public string DebugJson;

        internal readonly List<string> SegmentNames = new List<string>(4);
    }

    public enum UploadPoll
    {
        /// <summary>The background worker is still preparing the batch.</summary>
        Pending,
        /// <summary>There is nothing to upload right now.</summary>
        Empty,
        /// <summary>A batch is ready.</summary>
        Ready
    }

    /// <summary>
    /// Durable event queue. Events are persisted when they are enqueued and
    /// deleted only after the server confirms them.
    ///
    /// Layout of the storage directory:
    ///   NNNNNNNNNNNNN-NNNNNN.open  segment being appended to (one event per line,
    ///                              the exact JSON that is uploaded)
    ///   NNNNNNNNNNNNN-NNNNNN.seg   sealed segment, waiting for upload
    ///   seq                        reserved high-water mark of the event sequence
    ///   ozx_apm_offline_*.json     legacy (pre-APM-2) batch files, uploaded as-is
    ///   quarantine/                batches the server explicitly rejected (capped)
    /// A segment is sealed when it holds BatchSize events / MaxBatchBytes, is
    /// older than FlushIntervalSeconds, on Flush(), after a write error and at
    /// startup (".open" files left by a crash or kill). Consecutive small
    /// segments are merged into one upload.
    ///
    /// Threading: all file I/O happens on one background worker thread. The
    /// main thread only appends to an in-memory list under a lock. An event is
    /// written as soon as the worker wakes (immediately when the queue goes from
    /// empty to non-empty, at most PersistIntervalMs later in a burst).
    /// If the thread cannot be created the caller drives the worker with Tick().
    ///
    /// Nothing is written (no file, no directory) unless consent is Granted.
    /// Only one instance may own a directory at a time; a second instance on
    /// the same directory (e.g. re-initialising while the old worker is still
    /// running) falls back to memory-only mode.
    /// </summary>
    public class OfflineStorage
    {
        private const string SegmentExtension = ".seg";
        private const string OpenExtension = ".open";
        private const string SeqFileName = "seq";
        private const string QuarantineDirName = "quarantine";
        private const string LegacyFilePrefix = "ozx_apm_offline_";
        private const string LegacyFileExtension = ".json";
        private const long SeqReserveBlock = 1000;
        private const int CompressThresholdBytes = 1024;
        private const int IdleWaitMs = 60000;
        private const float WarningIntervalSeconds = 60f;
        private const int MaxPooledBufferBytes = 256 * 1024;

        /// <summary>Memory-only mode keeps at most this much (CLAUDE.md: &lt; 5MB resident in total).</summary>
        public const long MemoryOnlyMaxBytes = 1024 * 1024;
        /// <summary>Rejected batches kept for inspection.</summary>
        public const long MaxQuarantineBytes = 512 * 1024;
        public static readonly TimeSpan QuarantineTtl = TimeSpan.FromDays(7);

        private static readonly byte[] BatchPrefix = Encoding.UTF8.GetBytes("{\"events\":[");
        private static readonly byte[] BatchSuffix = Encoding.UTF8.GetBytes("]}");
        private static readonly byte[] LegacyEventMarker = Encoding.UTF8.GetBytes("{\"type\":\"");
        private static readonly byte[][] HighValueMarkers =
        {
            Encoding.UTF8.GetBytes("\"type\":\"exception\""),
            Encoding.UTF8.GetBytes("\"type\":\"crash\""),
            Encoding.UTF8.GetBytes("\"type\":\"startup\"")
        };
        private static readonly byte[] SeqMarker = Encoding.UTF8.GetBytes("\"seq\":");

        // Directories owned by a live instance in this process.
        private static readonly HashSet<string> s_ownedDirectories = new HashSet<string>(StringComparer.Ordinal);

        private readonly ApmConfig _config;
        private readonly string _storagePath;
        private readonly string _quarantinePath;
        private bool _persistToDisk;
        private bool _useWorkerThread;

        // ---- Shared between main thread and worker; guarded by _lock ----
        private readonly object _lock = new object();
        private List<BaseEvent> _pending = new List<BaseEvent>(64);
        private bool _flushRequested;
        private bool _clearRequested;
        private bool _uploadRequested;
        private UploadPoll _uploadState = UploadPoll.Empty;
        private UploadBatch _preparedUpload;
        private readonly List<Completion> _completions = new List<Completion>();
        private bool _stopRequested;
        private long _busyGen;              // bumped (under _lock) whenever new work is requested
        private long _droppedPendingEvents;

        private struct Completion
        {
            public UploadBatch Batch;
            public UploadOutcome Outcome;
        }

        // ---- Worker-only state ----
        private readonly AutoResetEvent _wake = new AutoResetEvent(false);
        // WaitIdle: generation of work the worker has fully finished. Separate lock and
        // Monitor (no OS wait handle) so Enqueue never makes a kernel call for it and
        // nothing slow runs while _lock is held.
        private readonly object _idleLock = new object();
        private long _idleGen;
        private Thread _thread;
        private List<BaseEvent> _drain = new List<BaseEvent>(64);
        private readonly List<Completion> _completionDrain = new List<Completion>();
        private readonly List<Segment> _segments = new List<Segment>(); // sealed, oldest first
        private readonly HashSet<string> _inFlight = new HashSet<string>(StringComparer.Ordinal);
        // Two open segments: exceptions/crashes/startup go to their own segments so
        // the storage cap can evict perf samples first even when exceptions arrive
        // continuously (a mixed segment would always count as high value).
        private readonly SegmentWriter _lowWriter = new SegmentWriter(false);
        private readonly SegmentWriter _highWriter = new SegmentWriter(true);

        private sealed class SegmentWriter
        {
            public readonly bool High;
            public Segment Open;
            public FileStream Stream;
            public readonly MemoryStream Buffer = new MemoryStream(16 * 1024);
            public readonly List<BaseEvent> Buffered = new List<BaseEvent>(32); // in Buffer, not yet on disk

            public SegmentWriter(bool high)
            {
                High = high;
            }

            public int OpenEvents => (Open != null ? Open.EventCount : 0) + Buffered.Count;
        }
        private long _totalBytes;
        private long _seq;
        private long _seqReserved;
        private int _segmentCounter;
        private bool _loaded;
        private bool _ownsDirectory;
        private bool _directoryReady;
        private long _droppedByCap;
        private int _lastSyncPumpTicks;
        private DateTime _writeRetryAtUtc = DateTime.MinValue;
        private int _writeFailures;
        private DateTime _lastWarningUtc = DateTime.MinValue;
        private int _suppressedWarnings;

        // Pooled buffers for the upload path.
        private byte[] _readBuffer = new byte[16 * 1024];
        private readonly MemoryStream _bodyBuffer = new MemoryStream(16 * 1024);
        private readonly MemoryStream _gzipBuffer = new MemoryStream(8 * 1024);

        /// <summary>
        /// Test seam: called with the segment stream before every append; may
        /// write partial data and throw to simulate a failing disk.
        /// </summary>
        internal static Action<Stream> BeforeWriteHook;
        /// <summary>Test seam: makes thread creation fail.</summary>
        internal static bool FailThreadStartForTests;

        private sealed class Segment
        {
            public string Name;
            public string Path;      // null in memory-only mode
            public MemoryStream Mem; // memory-only mode
            public long Bytes;
            public int EventCount = -1; // -1: unknown (loaded from a previous launch)
            public bool Legacy;
            public int HighValue = -1;  // -1 unknown, 0 no, 1 has exceptions/crashes/startup
            public DateTime CreatedUtc;
        }

        public OfflineStorage(ApmConfig config)
            : this(config,
                   Path.Combine(Application.persistentDataPath, "ozx_apm_offline"),
                   Application.platform != RuntimePlatform.WebGLPlayer)
        {
            // WebGL has no threads and no reliable file system: memory-only.
            if (Application.platform == RuntimePlatform.WebGLPlayer)
                _persistToDisk = false;
        }

        /// <param name="storagePath">Directory for segment files.</param>
        /// <param name="useWorkerThread">
        /// false: the caller drives the worker with Tick()/Pump() (tests, platforms without threads).
        /// </param>
        public OfflineStorage(ApmConfig config, string storagePath, bool useWorkerThread)
        {
            _config = config;
            _storagePath = Path.GetFullPath(storagePath);
            _quarantinePath = Path.Combine(_storagePath, QuarantineDirName);
            _persistToDisk = config.EnableOfflineStorage;
            _useWorkerThread = useWorkerThread;
        }

        public string StoragePath => _storagePath;

        /// <summary>True when events go to disk (false in memory-only mode).</summary>
        public bool PersistsToDisk => _persistToDisk;

        /// <summary>True when a background worker thread is running.</summary>
        public bool HasWorkerThread => _thread != null;

        /// <summary>Bytes currently held in local storage (worker's view).</summary>
        public long GetStorageSize() => Interlocked.Read(ref _totalBytes);

        /// <summary>Events dropped because the in-memory hand-off or the storage cap overflowed.</summary>
        public long DroppedEventCount => Interlocked.Read(ref _droppedPendingEvents) + Interlocked.Read(ref _droppedByCap);

        /// <summary>Highest sequence number assigned so far.</summary>
        public long LastSeq => Interlocked.Read(ref _seq);

        /// <summary>Events enqueued but not yet written by the worker.</summary>
        public int PendingEventCount
        {
            get
            {
                lock (_lock)
                {
                    return _pending.Count;
                }
            }
        }

        private long CapBytes => _persistToDisk
            ? Math.Max(4096, (long)_config.MaxOfflineStorageBytes)
            : Math.Min(MemoryOnlyMaxBytes, Math.Max(4096, (long)_config.MaxOfflineStorageBytes));

        /// <summary>
        /// Starts the background worker. Does no I/O on the calling thread. If
        /// the thread cannot be created, falls back to synchronous mode driven
        /// by Tick().
        /// </summary>
        public void Start()
        {
            if (!_useWorkerThread || _thread != null)
                return;

            try
            {
                if (FailThreadStartForTests)
                    throw new NotSupportedException("thread creation disabled for tests");

                var thread = new Thread(WorkerLoop)
                {
                    IsBackground = true,
                    Name = "OzxApm.Storage"
                };
                try
                {
                    thread.Priority = System.Threading.ThreadPriority.BelowNormal;
                }
                catch
                {
                    // Not supported everywhere; the default priority is fine.
                }
                thread.Start();
                _thread = thread;
            }
            catch (Exception ex)
            {
                _thread = null;
                _useWorkerThread = false;
                ApmClient.Log(LogLevel.Warning, $"Storage worker thread unavailable ({ex.Message}); writing from the main thread");
            }
        }

        /// <summary>
        /// Queues an event for persistence. Main thread; O(1), no I/O.
        /// </summary>
        public void Enqueue(BaseEvent evt)
        {
            if (evt == null || _config.Consent != ConsentState.Granted)
                return;

            bool wake;
            lock (_lock)
            {
                if (_stopRequested)
                    return;

                if (_pending.Count >= Math.Max(1, _config.MaxQueueSize))
                {
                    // Writer is not keeping up (e.g. disk full); drop the oldest.
                    _pending.RemoveAt(0);
                    _droppedPendingEvents++;
                }
                wake = _pending.Count == 0 || evt is ExceptionEvent || evt is CrashEvent;
                _pending.Add(evt);
                MarkBusy();
            }

            if (wake)
                _wake.Set();
        }

        /// <summary>
        /// Writes pending events and seals the open segment so it can be uploaded.
        /// </summary>
        public void Flush()
        {
            lock (_lock)
            {
                _flushRequested = true;
                MarkBusy();
            }
            _wake.Set();
        }

        /// <summary>
        /// Blocks until the worker has written everything requested so far, or the timeout elapses.
        /// </summary>
        public bool WaitIdle(int timeoutMs)
        {
            if (_thread == null)
            {
                Pump();
                return true;
            }

            long target;
            lock (_lock)
            {
                target = _busyGen;
            }
            var deadline = DateTime.UtcNow.AddMilliseconds(timeoutMs);
            lock (_idleLock)
            {
                while (_idleGen < target)
                {
                    int left = (int)(deadline - DateTime.UtcNow).TotalMilliseconds;
                    if (left <= 0)
                        return false;
                    Monitor.Wait(_idleLock, left);
                }
                return true;
            }
        }

        /// <summary>
        /// Deletes all locally stored events, including quarantined batches
        /// (async). Entry point for APM-14 (consent revoked).
        /// </summary>
        public void Clear()
        {
            lock (_lock)
            {
                _clearRequested = true;
                _pending.Clear();
                MarkBusy();
            }
            _wake.Set();
        }

        /// <summary>
        /// Asks the worker to prepare the next upload. Poll the result with PollUpload. Main thread.
        /// </summary>
        public void RequestUpload()
        {
            lock (_lock)
            {
                if (_uploadState == UploadPoll.Pending || _preparedUpload != null)
                    return;
                _uploadRequested = true;
                _uploadState = UploadPoll.Pending;
            }
            _wake.Set();
        }

        /// <summary>
        /// Returns the state of the last RequestUpload. When Ready, the batch is
        /// handed over and must later be passed to CompleteUpload.
        /// </summary>
        public UploadPoll PollUpload(out UploadBatch batch)
        {
            if (_thread == null)
                Pump();

            lock (_lock)
            {
                batch = null;
                var state = _uploadState;
                if (state == UploadPoll.Ready)
                {
                    batch = _preparedUpload;
                    _preparedUpload = null;
                    _uploadState = UploadPoll.Empty;
                }
                return state;
            }
        }

        /// <summary>
        /// Reports the outcome of an upload. Delivered deletes the batch,
        /// Rejected moves it to the quarantine directory, Retry keeps it.
        /// </summary>
        public void CompleteUpload(UploadBatch batch, UploadOutcome outcome)
        {
            if (batch == null)
                return;

            lock (_lock)
            {
                _completions.Add(new Completion { Batch = batch, Outcome = outcome });
                MarkBusy();
            }
            _wake.Set();
        }

        /// <summary>delivered=true: Delivered; false: Retry.</summary>
        public void CompleteUpload(UploadBatch batch, bool delivered)
        {
            CompleteUpload(batch, delivered ? UploadOutcome.Delivered : UploadOutcome.Retry);
        }

        /// <summary>
        /// Writes what is pending, seals the open segment and stops the worker.
        /// Waits at most timeoutMs; anything not written by then is lost.
        /// </summary>
        public void Shutdown(int timeoutMs)
        {
            lock (_lock)
            {
                _flushRequested = true;
                _stopRequested = true;
            }
            _wake.Set();

            var thread = _thread;
            if (thread != null)
            {
                try { thread.Join(timeoutMs); } catch { }
                // If the join timed out the worker releases the directory when it exits.
            }
            else
            {
                Pump();
                try { CloseOpenStreams(); } catch { }
                ReleaseDirectory();
            }
        }

        /// <summary>
        /// Call once per frame from the main thread. Only does work when there is
        /// no worker thread, where it pumps the worker every PersistIntervalMs.
        /// </summary>
        public void Tick()
        {
            if (_thread != null)
                return;

            int now = Environment.TickCount;
            if (unchecked(now - _lastSyncPumpTicks) < _config.PersistIntervalMs)
                return;
            _lastSyncPumpTicks = now;
            Pump();
        }

        /// <summary>
        /// Runs one worker iteration on the calling thread. Used by tests and on
        /// platforms without threads; with a worker thread it is called by the thread.
        /// </summary>
        public void Pump()
        {
            try
            {
                PumpInternal();
            }
            catch (Exception ex)
            {
                // Never let a storage problem escape into the game.
                Warn($"Storage worker error: {ex.GetType().Name}: {ex.Message}");
            }
        }

        /// <summary>Warnings actually logged (after rate limiting).</summary>
        internal int WarningsLogged { get; private set; }

        /// <summary>Test seam: allow the next write attempt now.</summary>
        internal void ResetWriteBackoffForTests()
        {
            _writeRetryAtUtc = DateTime.MinValue;
        }

        /// <summary>Test seam: drop the instance as if the process died (no flush, no seal).</summary>
        internal void SimulateCrashForTests()
        {
            CloseOpenStreams();
            ReleaseDirectory();
        }

        // Called under _lock: just a counter, no kernel call on the main thread.
        private void MarkBusy()
        {
            _busyGen++;
        }

        // Called by the worker WITHOUT holding _lock: everything up to generation
        // `gen` is done.
        private void SignalIdle(long gen)
        {
            lock (_idleLock)
            {
                if (gen <= _idleGen)
                    return;
                _idleGen = gen;
                Monitor.PulseAll(_idleLock);
            }
        }

        private void WorkerLoop()
        {
            try
            {
                while (true)
                {
                    Pump();

                    bool stop;
                    lock (_lock)
                    {
                        stop = _stopRequested && _pending.Count == 0;
                    }
                    if (stop)
                        break;

                    _wake.WaitOne(ComputeWaitMs());
                }
            }
            catch (Exception ex)
            {
                Warn($"Storage worker stopped: {ex.Message}");
            }
            finally
            {
                try { CloseOpenStreams(); } catch { }
                ReleaseDirectory();
                long gen;
                lock (_lock)
                {
                    gen = _busyGen;
                }
                SignalIdle(gen); // stopped: nothing more will happen
            }
        }

        /// <summary>
        /// How long the worker may sleep: briefly while events are pending, until
        /// the open segment's seal deadline, otherwise a long idle wait (any
        /// Enqueue/Flush/upload request wakes it immediately).
        /// </summary>
        private int ComputeWaitMs()
        {
            int persist = Math.Max(10, _config.PersistIntervalMs);
            var now = DateTime.UtcNow;
            lock (_lock)
            {
                if (_pending.Count > 0 || _flushRequested || _clearRequested || _uploadRequested || _completions.Count > 0)
                {
                    if (_writeRetryAtUtc > now && _pending.Count > 0)
                        return (int)Math.Min(IdleWaitMs, Math.Max(persist, (_writeRetryAtUtc - now).TotalMilliseconds));
                    return persist;
                }
            }
            double wait = IdleWaitMs;
            foreach (var w in Writers)
            {
                if (w.Open != null && w.Open.EventCount > 0)
                    wait = Math.Min(wait, (w.Open.CreatedUtc.AddSeconds(_config.FlushIntervalSeconds) - now).TotalMilliseconds);
            }
            return (int)Math.Max(10, wait);
        }

        private void PumpInternal()
        {
            if (!_loaded)
            {
                _loaded = true; // once, even if the scan fails
                LoadExisting();
            }

            bool flush, clear, upload;
            bool writeAllowed = DateTime.UtcNow >= _writeRetryAtUtc;
            lock (_lock)
            {
                if (writeAllowed)
                {
                    var swap = _drain;
                    _drain = _pending;
                    _pending = swap;
                }

                flush = _flushRequested;
                clear = _clearRequested;
                upload = _uploadRequested;
                _flushRequested = false;
                _clearRequested = false;
                _uploadRequested = false;

                _completionDrain.Clear();
                _completionDrain.AddRange(_completions);
                _completions.Clear();
            }

            try
            {
                for (int i = 0; i < _completionDrain.Count; i++)
                    ApplyCompletion(_completionDrain[i]);

                if (clear)
                    DeleteAll();

                if (_drain.Count > 0)
                    WriteEvents(_drain);

                foreach (var w in Writers)
                {
                    if (w.Open != null && w.Open.EventCount > 0 &&
                        (flush || (DateTime.UtcNow - w.Open.CreatedUtc).TotalSeconds >= _config.FlushIntervalSeconds))
                    {
                        SealOpen(w);
                    }
                }

                EnforceCap();

                if (flush && _lowWriter.Buffered.Count == 0 && _highWriter.Buffered.Count == 0)
                {
                    bool nothingPending;
                    lock (_lock)
                    {
                        nothingPending = _pending.Count == 0;
                    }
                    if (nothingPending)
                        ShrinkSeqReservation();
                }

                if (upload)
                    PrepareUpload();
            }
            finally
            {
                _drain.Clear();
                _completionDrain.Clear();
                long idleGen = -1;
                lock (_lock)
                {
                    if (upload && _uploadState == UploadPoll.Pending && _preparedUpload == null)
                        _uploadState = UploadPoll.Empty; // PrepareUpload threw
                    if (_pending.Count == 0 && !_flushRequested && !_clearRequested && _completions.Count == 0)
                        idleGen = _busyGen;
                }
                if (idleGen >= 0)
                    SignalIdle(idleGen);
            }
        }

        private void Warn(string message)
        {
            // Rate limited: a full disk must not flood the log.
            var now = DateTime.UtcNow;
            if ((now - _lastWarningUtc).TotalSeconds < WarningIntervalSeconds)
            {
                _suppressedWarnings++;
                return;
            }
            if (_suppressedWarnings > 0)
                message += $" ({_suppressedWarnings} similar warnings suppressed)";
            _suppressedWarnings = 0;
            _lastWarningUtc = now;
            WarningsLogged++;
            ApmClient.Log(LogLevel.Warning, message);
        }

        // ------------------------------------------------------------------
        // Startup scan (worker thread)
        // ------------------------------------------------------------------

        private void LoadExisting()
        {
            if (!_persistToDisk)
                return;

            lock (s_ownedDirectories)
            {
                if (!s_ownedDirectories.Add(_storagePath))
                {
                    _persistToDisk = false;
                    ApmClient.Log(LogLevel.Warning,
                        "Another APM storage instance is still using " + _storagePath + "; this one keeps events in memory only");
                    return;
                }
                _ownsDirectory = true;
            }

            if (!Directory.Exists(_storagePath))
                return;

            _directoryReady = true;
            var files = Directory.GetFiles(_storagePath);
            Array.Sort(files, StringComparer.Ordinal);

            foreach (var file in files)
            {
                string name = Path.GetFileName(file);
                try
                {
                    if (name.EndsWith(OpenExtension, StringComparison.Ordinal))
                    {
                        // Left behind by a crash or kill: seal it so it gets uploaded.
                        string sealedPath = Path.ChangeExtension(file, SegmentExtension);
                        try
                        {
                            File.Move(file, sealedPath);
                            AddLoadedSegment(sealedPath, false);
                        }
                        catch (IOException)
                        {
                            AddLoadedSegment(file, false);
                        }
                    }
                    else if (name.EndsWith(SegmentExtension, StringComparison.Ordinal))
                    {
                        AddLoadedSegment(file, false);
                    }
                    else if (name.StartsWith(LegacyFilePrefix, StringComparison.Ordinal) &&
                             name.EndsWith(LegacyFileExtension, StringComparison.Ordinal))
                    {
                        AddLoadedSegment(file, true);
                    }
                }
                catch (Exception ex)
                {
                    Warn($"Skipping unreadable event file {name}: {ex.Message}");
                }
            }

            // Legacy files sort after numbered segments by name; they are older, so upload them first.
            _segments.Sort((a, b) =>
            {
                if (a.Legacy != b.Legacy) return a.Legacy ? -1 : 1;
                return string.CompareOrdinal(a.Name, b.Name);
            });

            _seq = Math.Max(ReadSeqFile(), MaxSeqInNewestSegments());
            _seqReserved = _seq;

            SweepQuarantine();

            if (_segments.Count > 0)
                ApmClient.Log(LogLevel.Info, $"Found {_segments.Count} stored event batches ({_totalBytes} bytes) to upload");
        }

        private void ReleaseDirectory()
        {
            if (!_ownsDirectory)
                return;
            lock (s_ownedDirectories)
            {
                s_ownedDirectories.Remove(_storagePath);
            }
            _ownsDirectory = false;
        }

        private void AddLoadedSegment(string path, bool legacy)
        {
            long bytes = new FileInfo(path).Length;
            if (bytes == 0)
            {
                File.Delete(path);
                return;
            }

            _segments.Add(new Segment
            {
                Name = Path.GetFileName(path),
                Path = path,
                Bytes = bytes,
                Legacy = legacy
            });
            _totalBytes += bytes;
        }

        private long ReadSeqFile()
        {
            long best = 0;
            foreach (var name in new[] { SeqFileName, SeqFileName + ".tmp" })
            {
                try
                {
                    string path = Path.Combine(_storagePath, name);
                    if (!File.Exists(path))
                        continue;
                    string text = File.ReadAllText(path).Trim();
                    if (long.TryParse(text, NumberStyles.Integer, CultureInfo.InvariantCulture, out long value) && value > best)
                        best = value;
                }
                catch
                {
                    // Unreadable: the segment scan below covers it.
                }
            }
            return best;
        }

        /// <summary>
        /// Highest "seq" in the most recent segments. Protects monotonicity when the
        /// seq file was lost or is stale. Seq only grows, but high- and low-value
        /// events go to different segments that are open at the same time, so the
        /// newest segment alone is not enough: scan every segment created within
        /// one seal interval (plus margin) of the newest one.
        /// </summary>
        private long MaxSeqInNewestSegments()
        {
            long newestMs = -1;
            for (int s = _segments.Count - 1; s >= 0 && newestMs < 0; s--)
            {
                if (!_segments[s].Legacy)
                    newestMs = SegmentCreatedMs(_segments[s].Name);
            }
            if (newestMs < 0)
                return 0;

            long windowMs = (long)(Math.Max(1f, _config.FlushIntervalSeconds) * 1000) + 60000;
            long max = 0;
            for (int s = _segments.Count - 1; s >= 0; s--)
            {
                var segment = _segments[s];
                if (segment.Legacy)
                    continue;
                long created = SegmentCreatedMs(segment.Name);
                if (created >= 0 && created < newestMs - windowMs)
                    continue;
                if (!ReadSegment(segment, out int length))
                    continue;
                for (int i = IndexOf(_readBuffer, 0, length, SeqMarker); i >= 0; i = IndexOf(_readBuffer, i + 1, length, SeqMarker))
                {
                    long value = 0;
                    int j = i + SeqMarker.Length;
                    while (j < length && _readBuffer[j] >= (byte)'0' && _readBuffer[j] <= (byte)'9' && value < long.MaxValue / 10)
                    {
                        value = value * 10 + (_readBuffer[j] - (byte)'0');
                        j++;
                    }
                    if (value > max)
                        max = value;
                }
            }
            return max;
        }

        /// <summary>Creation time (Unix ms) encoded in a segment name, or -1.</summary>
        private static long SegmentCreatedMs(string name)
        {
            int dash = name.IndexOf('-');
            if (dash <= 0)
                return -1;
            return long.TryParse(name.Substring(0, dash), NumberStyles.None, CultureInfo.InvariantCulture, out long ms) ? ms : -1;
        }

        // ------------------------------------------------------------------
        // Writing (worker thread)
        // ------------------------------------------------------------------

        private void WriteEvents(List<BaseEvent> events)
        {
            // Second consent gate: consent may have been revoked after enqueue.
            if (_config.Consent != ConsentState.Granted)
                return;

            int batchSize = Math.Max(1, _config.BatchSize);
            int maxBytes = Math.Max(1024, _config.MaxBatchBytes);
            int next = 0;                 // index of the event being written
            bool currentBuffered = false; // events[next] already sits in a writer buffer

            try
            {
                int needSeq = 0;
                for (int i = 0; i < events.Count; i++)
                {
                    if (events[i].seq == 0)
                        needSeq++;
                }
                // Do not hand out sequence numbers unless their reservation is durable.
                ReserveSeq(needSeq);

                for (; next < events.Count; next++)
                {
                    currentBuffered = false;
                    var evt = events[next];
                    if (evt.seq == 0)
                        evt.seq = ++_seq; // kept if the write is retried
                    if (string.IsNullOrEmpty(evt.event_id))
                        evt.event_id = Guid.NewGuid().ToString("N"); // client-unique id, generated off the main thread

                    string line;
                    try
                    {
                        line = JsonSerializer.SerializeEvent(evt);
                    }
                    catch (Exception ex)
                    {
                        Warn($"Failed to serialize {evt.type} event: {ex.Message}");
                        continue;
                    }

                    var w = IsHighValueEvent(evt) ? _highWriter : _lowWriter;
                    int lineBytes = Encoding.UTF8.GetByteCount(line) + 1;
                    if (w.OpenEvents > 0 && w.Open.Bytes + w.Buffer.Length + lineBytes > maxBytes)
                    {
                        CommitLineBuffer(w);
                        SealOpen(w);
                    }

                    if (w.Open == null)
                        OpenSegment(w);

                    var bytes = Encoding.UTF8.GetBytes(line);
                    w.Buffer.Write(bytes, 0, bytes.Length);
                    w.Buffer.WriteByte((byte)'\n');
                    w.Buffered.Add(evt);
                    currentBuffered = true;

                    if (w.OpenEvents >= batchSize)
                    {
                        CommitLineBuffer(w);
                        SealOpen(w);
                    }
                }

                CommitLineBuffer(_highWriter);
                CommitLineBuffer(_lowWriter);
                _writeFailures = 0;
            }
            catch (Exception ex)
            {
                // Events still in a writer buffer are retried from there; the
                // rest start at the first event that was never buffered.
                OnWriteFailure(ex, events, currentBuffered ? next + 1 : next);
            }
        }

        private SegmentWriter[] _writers;
        private SegmentWriter[] Writers => _writers ?? (_writers = new[] { _highWriter, _lowWriter });

        private static bool IsHighValueEvent(BaseEvent evt)
        {
            return evt is ExceptionEvent || evt is CrashEvent || evt is StartupEvent;
        }

        /// <summary>
        /// A write failed (disk full, I/O error, seq reservation not durable).
        /// Open segments were truncated back to their last complete line (or, if
        /// that failed too, are abandoned so nothing is ever appended to a cut-off
        /// line). Events that did not reach the disk go back to the front of the
        /// queue with their sequence numbers, and writing is retried with backoff.
        /// </summary>
        private void OnWriteFailure(Exception ex, List<BaseEvent> events, int notBuffered)
        {
            // Not on disk: whatever is still buffered in either writer, then the
            // events that were never buffered.
            var retry = new List<BaseEvent>();
            foreach (var w in Writers)
            {
                retry.AddRange(w.Buffered);
                w.Buffered.Clear();
                w.Buffer.SetLength(0);
                AbandonOpen(w);
            }
            retry.Sort((a, b) => a.seq.CompareTo(b.seq));
            if (notBuffered < events.Count)
                retry.AddRange(events.GetRange(notBuffered, events.Count - notBuffered));

            _writeFailures++;
            double delay = Math.Min(30.0, 0.5 * Math.Pow(2, Math.Min(_writeFailures - 1, 6)));
            _writeRetryAtUtc = DateTime.UtcNow.AddSeconds(delay);
            Warn($"Failed to write events ({ex.GetType().Name}: {ex.Message}); retrying in {delay:F1}s");

            if (retry.Count == 0)
                return;

            lock (_lock)
            {
                _pending.InsertRange(0, retry);
                int max = Math.Max(1, _config.MaxQueueSize);
                if (_pending.Count > max)
                {
                    int drop = _pending.Count - max;
                    _pending.RemoveRange(0, drop);
                    _droppedPendingEvents += drop;
                }
            }
        }

        private void ReserveSeq(int count)
        {
            if (_seq + count <= _seqReserved)
                return;

            long reserved = _seq + count + SeqReserveBlock;
            if (_persistToDisk)
            {
                // Make the reservation durable before any event uses it, so a crash
                // can only create gaps, never reuse a number. Throws on failure.
                WriteSeqFile(reserved);
            }
            _seqReserved = reserved;
        }

        /// <summary>
        /// On Flush (app paused, quit, explicit flush) give back the unused part of
        /// the reservation, so the next launch continues at exactly the next number.
        /// Then a gap in seq only appears after the process died in the foreground
        /// (crash, kill) with events still reserved.
        /// </summary>
        private void ShrinkSeqReservation()
        {
            if (!_persistToDisk || _seqReserved <= _seq || !_directoryReady)
                return;
            try
            {
                WriteSeqFile(_seq);
                _seqReserved = _seq;
            }
            catch (Exception ex)
            {
                // Keeping the larger reservation is safe.
                Warn($"Failed to update event sequence file: {ex.Message}");
            }
        }

        private void WriteSeqFile(long value)
        {
            EnsureDirectory();
            string path = Path.Combine(_storagePath, SeqFileName);
            string tmp = path + ".tmp";
            var bytes = Encoding.ASCII.GetBytes(value.ToString(CultureInfo.InvariantCulture));
            using (var fs = new FileStream(tmp, FileMode.Create, FileAccess.Write, FileShare.None))
            {
                fs.Write(bytes, 0, bytes.Length);
                fs.Flush(true);
            }
            if (File.Exists(path))
                File.Delete(path);
            File.Move(tmp, path);
        }

        private void OpenSegment(SegmentWriter w)
        {
            var now = DateTime.UtcNow;
            long ms = new DateTimeOffset(now).ToUnixTimeMilliseconds();
            string baseName = SegmentBaseName(ms);

            var segment = new Segment { CreatedUtc = now, EventCount = 0, HighValue = w.High ? 1 : 0 };

            if (_persistToDisk)
            {
                EnsureDirectory();
                // Names sort by creation time; never reuse a name left by a previous launch.
                while (File.Exists(Path.Combine(_storagePath, baseName + SegmentExtension)) ||
                       File.Exists(Path.Combine(_storagePath, baseName + OpenExtension)))
                {
                    baseName = SegmentBaseName(ms);
                }
                segment.Name = baseName + SegmentExtension;
                segment.Path = Path.Combine(_storagePath, baseName + OpenExtension);
                // Unbuffered (we batch in w.Buffer): a failed write cannot leave
                // bytes in a FileStream buffer that a later Dispose would flush.
                w.Stream = new FileStream(segment.Path, FileMode.CreateNew, FileAccess.Write, FileShare.Read, 1);
            }
            else
            {
                segment.Name = baseName + SegmentExtension;
                segment.Mem = new MemoryStream();
            }
            w.Open = segment;
        }

        private string SegmentBaseName(long unixMs)
        {
            return unixMs.ToString("D13", CultureInfo.InvariantCulture) + "-" +
                   (_segmentCounter++ % 1000000).ToString("D6", CultureInfo.InvariantCulture);
        }

        /// <summary>
        /// Appends the buffered lines to the writer's open segment. On failure the
        /// file is truncated back to the last committed length and the exception rethrown.
        /// </summary>
        private void CommitLineBuffer(SegmentWriter w)
        {
            if (w.Buffer.Length == 0 || w.Open == null)
                return;

            var open = w.Open;
            int length = (int)w.Buffer.Length;
            long committedLength = open.Bytes;
            try
            {
                BeforeWriteHook?.Invoke(open.Mem != null ? (Stream)open.Mem : w.Stream);
                if (open.Mem != null)
                {
                    open.Mem.Write(w.Buffer.GetBuffer(), 0, length);
                }
                else
                {
                    w.Stream.Write(w.Buffer.GetBuffer(), 0, length);
                    // Hand the bytes to the OS so they survive the process being killed.
                    w.Stream.Flush();
                }
            }
            catch
            {
                TruncateOpen(w, committedLength);
                throw;
            }

            open.Bytes += length;
            _totalBytes += length;
            open.EventCount += w.Buffered.Count;
            w.Buffer.SetLength(0);
            w.Buffered.Clear();
        }

        private static void TruncateOpen(SegmentWriter w, long length)
        {
            try
            {
                if (w.Open.Mem != null)
                {
                    w.Open.Mem.SetLength(length);
                }
                else if (w.Stream != null)
                {
                    w.Stream.Flush();
                    w.Stream.SetLength(length);
                    w.Stream.Flush();
                }
            }
            catch
            {
                // AbandonOpen() makes sure nothing is appended to a cut-off line.
            }
        }

        /// <summary>
        /// Stops writing to the writer's open segment: keeps what it holds
        /// (sealed), or deletes it if empty.
        /// </summary>
        private void AbandonOpen(SegmentWriter w)
        {
            if (w.Open == null)
                return;
            try
            {
                SealOpen(w);
            }
            catch
            {
                CloseStream(w);
                w.Open = null;
            }
        }

        private void SealOpen(SegmentWriter w)
        {
            if (w.Open == null)
                return;

            var segment = w.Open;
            w.Open = null;
            CloseStream(w);

            if (segment.EventCount == 0)
            {
                DeleteSegmentData(segment);
                return;
            }

            if (segment.Path != null)
            {
                string sealedPath = Path.Combine(_storagePath, segment.Name);
                try
                {
                    File.Move(segment.Path, sealedPath);
                    segment.Path = sealedPath;
                }
                catch (IOException ex)
                {
                    // Still uploadable under its .open name.
                    segment.Name = Path.GetFileName(segment.Path);
                    ApmClient.Log(LogLevel.Debug, $"Could not seal segment: {ex.Message}");
                }
            }

            // Keep _segments in creation order (oldest first), as after a restart.
            int at = _segments.Count;
            while (at > 0 && !_segments[at - 1].Legacy && string.CompareOrdinal(_segments[at - 1].Name, segment.Name) > 0)
                at--;
            _segments.Insert(at, segment);
        }

        private static void CloseStream(SegmentWriter w)
        {
            if (w.Stream == null)
                return;
            try { w.Stream.Dispose(); } catch { }
            w.Stream = null;
        }

        private void CloseOpenStreams()
        {
            CloseStream(_lowWriter);
            CloseStream(_highWriter);
        }

        private void EnsureDirectory()
        {
            if (_directoryReady)
                return;
            Directory.CreateDirectory(_storagePath);
            _directoryReady = true;
        }

        // ------------------------------------------------------------------
        // Cap, completion and deletion (worker thread)
        // ------------------------------------------------------------------

        /// <summary>
        /// Keeps local storage under the cap by dropping the oldest segments,
        /// low-value ones (no exception/crash/startup events) first. Segments
        /// currently being uploaded and the open segment are never evicted.
        /// </summary>
        private void EnforceCap()
        {
            long cap = CapBytes;
            while (_totalBytes > cap)
            {
                int victim = -1;
                for (int pass = 0; pass < 2 && victim < 0; pass++)
                {
                    for (int i = 0; i < _segments.Count; i++)
                    {
                        var s = _segments[i];
                        if (_inFlight.Contains(s.Name))
                            continue;
                        if (pass == 0 && IsHighValue(s))
                            continue;
                        victim = i;
                        break;
                    }
                }
                if (victim < 0)
                    break;

                var oldest = _segments[victim];
                _segments.RemoveAt(victim);
                _droppedByCap += CountEvents(oldest);
                DeleteSegmentData(oldest);
                ApmClient.Log(LogLevel.Debug, $"Storage full, dropped batch {oldest.Name}");
            }
        }

        private bool IsHighValue(Segment s)
        {
            if (s.HighValue < 0)
            {
                s.HighValue = 0;
                if (ReadSegment(s, out int length))
                {
                    for (int m = 0; m < HighValueMarkers.Length; m++)
                    {
                        if (IndexOf(_readBuffer, 0, length, HighValueMarkers[m]) >= 0)
                        {
                            s.HighValue = 1;
                            break;
                        }
                    }
                }
            }
            return s.HighValue == 1;
        }

        private int CountEvents(Segment s)
        {
            if (s.EventCount >= 0)
                return s.EventCount;
            if (!ReadSegment(s, out int length))
                return 0;
            if (s.Legacy)
                return CountOccurrences(_readBuffer, length, LegacyEventMarker);
            int lines = 0;
            for (int i = 0; i < length; i++)
            {
                if (_readBuffer[i] == (byte)'\n')
                    lines++;
            }
            return lines;
        }

        private void ApplyCompletion(Completion completion)
        {
            var names = completion.Batch.SegmentNames;
            for (int n = 0; n < names.Count; n++)
            {
                _inFlight.Remove(names[n]);
                if (completion.Outcome == UploadOutcome.Retry)
                    continue;

                int index = FindSegment(names[n]);
                if (index < 0)
                    continue;
                var segment = _segments[index];
                _segments.RemoveAt(index);
                if (completion.Outcome == UploadOutcome.Rejected)
                    Quarantine(segment);
                else
                    DeleteSegmentData(segment);
            }
        }

        private int FindSegment(string name)
        {
            for (int i = 0; i < _segments.Count; i++)
            {
                if (_segments[i].Name == name)
                    return i;
            }
            return -1;
        }

        /// <summary>
        /// Moves a batch the server explicitly rejected out of the upload queue
        /// into quarantine/ (kept for inspection, capped by size and age).
        /// </summary>
        private void Quarantine(Segment segment)
        {
            if (segment.Path == null)
            {
                DeleteSegmentData(segment);
                return;
            }
            try
            {
                Directory.CreateDirectory(_quarantinePath);
                string target = Path.Combine(_quarantinePath, segment.Name);
                if (File.Exists(target))
                    File.Delete(target);
                File.Move(segment.Path, target);
                _totalBytes -= segment.Bytes;
                if (_totalBytes < 0) _totalBytes = 0;
                Warn($"Server rejected batch {segment.Name}; moved to quarantine");
            }
            catch (Exception ex)
            {
                Warn($"Failed to quarantine {segment.Name}: {ex.Message}");
                DeleteSegmentData(segment);
            }
            SweepQuarantine();
        }

        private void SweepQuarantine()
        {
            try
            {
                if (!Directory.Exists(_quarantinePath))
                    return;
                var files = Directory.GetFiles(_quarantinePath);
                Array.Sort(files, StringComparer.Ordinal); // oldest first (names start with the creation time)
                var infos = new List<FileInfo>(files.Length);
                long total = 0;
                var cutoff = DateTime.UtcNow - QuarantineTtl;
                foreach (var f in files)
                {
                    var info = new FileInfo(f);
                    if (info.LastWriteTimeUtc < cutoff)
                    {
                        info.Delete();
                        continue;
                    }
                    infos.Add(info);
                    total += info.Length;
                }
                for (int i = 0; i < infos.Count && total > MaxQuarantineBytes; i++)
                {
                    total -= infos[i].Length;
                    infos[i].Delete();
                }
            }
            catch (Exception ex)
            {
                Warn($"Failed to clean quarantine: {ex.Message}");
            }
        }

        private void DeleteSegmentData(Segment segment)
        {
            _totalBytes -= segment.Bytes;
            if (_totalBytes < 0) _totalBytes = 0;

            if (segment.Path != null)
            {
                try
                {
                    if (File.Exists(segment.Path))
                        File.Delete(segment.Path);
                }
                catch (Exception ex)
                {
                    Warn($"Failed to delete {segment.Name}: {ex.Message}");
                }
            }
            segment.Mem = null;
        }

        private void DeleteAll()
        {
            CloseOpenStreams();
            foreach (var w in Writers)
            {
                if (w.Open != null)
                {
                    DeleteSegmentData(w.Open);
                    w.Open = null;
                }
                w.Buffer.SetLength(0);
                w.Buffered.Clear();
            }
            for (int i = 0; i < _segments.Count; i++)
                DeleteSegmentData(_segments[i]);
            _segments.Clear();
            _inFlight.Clear();
            _totalBytes = 0;

            try
            {
                if (Directory.Exists(_quarantinePath))
                    Directory.Delete(_quarantinePath, true);
            }
            catch (Exception ex)
            {
                Warn($"Failed to clear quarantine: {ex.Message}");
            }

            lock (_lock)
            {
                _preparedUpload = null;
                if (_uploadState == UploadPoll.Ready)
                    _uploadState = UploadPoll.Empty;
            }
        }

        // ------------------------------------------------------------------
        // Upload preparation (worker thread)
        // ------------------------------------------------------------------

        private void PrepareUpload()
        {
            UploadBatch batch = null;

            if (_config.Consent == ConsentState.Granted && _inFlight.Count == 0)
            {
                while (_segments.Count > 0 && batch == null)
                {
                    batch = BuildBatch();
                }
            }

            if (batch != null)
            {
                for (int i = 0; i < batch.SegmentNames.Count; i++)
                    _inFlight.Add(batch.SegmentNames[i]);
            }

            lock (_lock)
            {
                _preparedUpload = batch;
                _uploadState = batch != null ? UploadPoll.Ready : UploadPoll.Empty;
            }
        }

        /// <summary>
        /// Builds one request from the oldest segment(s). Returns null after
        /// dropping a segment with no valid event, so the caller tries the next.
        /// </summary>
        private UploadBatch BuildBatch()
        {
            var first = _segments[0];
            var batch = new UploadBatch { SegmentName = first.Name };

            _bodyBuffer.SetLength(0);

            if (first.Legacy)
            {
                if (!ReadSegment(first, out int length) || !LooksLikeObject(_readBuffer, length))
                {
                    DropUnreadable(0);
                    return null;
                }
                // Pre-APM-2 files already are a full {"events":[...]} body; the
                // server salvages the well-formed events if some are broken.
                _bodyBuffer.Write(_readBuffer, 0, length);
                batch.EventCount = Math.Max(1, CountOccurrences(_readBuffer, length, LegacyEventMarker));
                batch.SegmentNames.Add(first.Name);
                return FinishBatch(batch);
            }

            int batchSize = Math.Max(1, _config.BatchSize);
            int maxBytes = Math.Max(1024, _config.MaxBatchBytes);
            _bodyBuffer.Write(BatchPrefix, 0, BatchPrefix.Length);

            for (int s = 0; s < _segments.Count; s++)
            {
                var segment = _segments[s];
                if (segment.Legacy)
                    break;
                if (s > 0 && segment.EventCount >= 0 &&
                    (batch.EventCount + segment.EventCount > batchSize ||
                     _bodyBuffer.Length + segment.Bytes > maxBytes))
                    break;

                if (!ReadSegment(segment, out int length))
                {
                    if (s == 0)
                    {
                        DropUnreadable(0);
                        return null;
                    }
                    break;
                }

                long mark = _bodyBuffer.Length;
                int added = AppendValidLines(_readBuffer, length, batch.EventCount > 0);
                if (added == 0)
                {
                    _bodyBuffer.SetLength(mark);
                    if (s == 0)
                    {
                        // Nothing valid inside (e.g. only a cut-off line): drop it.
                        DropUnreadable(0);
                        return null;
                    }
                    break;
                }
                if (s > 0 && (batch.EventCount + added > batchSize || _bodyBuffer.Length + BatchSuffix.Length > maxBytes))
                {
                    _bodyBuffer.SetLength(mark); // would overflow the request
                    break;
                }
                batch.EventCount += added;
                batch.SegmentNames.Add(segment.Name);
            }

            _bodyBuffer.Write(BatchSuffix, 0, BatchSuffix.Length);
            return FinishBatch(batch);
        }

        private void DropUnreadable(int index)
        {
            var segment = _segments[index];
            _segments.RemoveAt(index);
            _droppedByCap += Math.Max(0, segment.EventCount);
            DeleteSegmentData(segment);
        }

        private UploadBatch FinishBatch(UploadBatch batch)
        {
            int length = (int)_bodyBuffer.Length;
            batch.UncompressedBytes = length;

            if (_config.LogLevel >= LogLevel.Debug)
                batch.DebugJson = Encoding.UTF8.GetString(_bodyBuffer.GetBuffer(), 0, length);

            if (_config.EnableCompression && length > CompressThresholdBytes)
            {
                try
                {
                    _gzipBuffer.SetLength(0);
                    using (var gzip = new GZipStream(_gzipBuffer, CompressionMode.Compress, true))
                    {
                        gzip.Write(_bodyBuffer.GetBuffer(), 0, length);
                    }
                    batch.Payload = _gzipBuffer.ToArray();
                    batch.Compressed = true;
                }
                catch (Exception ex)
                {
                    Warn($"Compression failed: {ex.Message}");
                }
            }
            if (batch.Payload == null)
                batch.Payload = _bodyBuffer.ToArray();

            TrimPooledBuffers();
            return batch;
        }

        /// <summary>
        /// Appends every complete, well-formed JSON-object line to the body. A last
        /// line without a newline was cut off by a crash; a line that is not a
        /// valid object (e.g. a cut-off line that later data was appended to) is
        /// dropped so it cannot invalidate the whole request.
        /// </summary>
        private int AppendValidLines(byte[] raw, int length, bool needComma)
        {
            int added = 0;
            int lineStart = 0;
            for (int i = 0; i < length; i++)
            {
                if (raw[i] != (byte)'\n')
                    continue;

                int start = lineStart;
                int end = i;
                lineStart = i + 1;
                if (end > start && raw[end - 1] == (byte)'\r')
                    end--;
                if (!JsonValidator.IsObject(raw, start, end))
                    continue;

                if (needComma || added > 0)
                    _bodyBuffer.WriteByte((byte)',');
                _bodyBuffer.Write(raw, start, end - start);
                added++;
            }
            return added;
        }

        /// <summary>Reads a segment into the pooled read buffer.</summary>
        private bool ReadSegment(Segment segment, out int length)
        {
            length = 0;
            try
            {
                if (segment.Mem != null)
                {
                    length = (int)segment.Mem.Length;
                    EnsureReadBuffer(length);
                    Buffer.BlockCopy(segment.Mem.GetBuffer(), 0, _readBuffer, 0, length);
                    return true;
                }
                if (segment.Path == null)
                    return false;

                using (var fs = new FileStream(segment.Path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite))
                {
                    long size = fs.Length;
                    if (size > int.MaxValue / 2)
                        return false;
                    EnsureReadBuffer((int)size);
                    int read;
                    while (length < size && (read = fs.Read(_readBuffer, length, (int)size - length)) > 0)
                        length += read;
                }
                return true;
            }
            catch (Exception ex)
            {
                Warn($"Failed to read {segment.Name}: {ex.Message}");
                return false;
            }
        }

        private void EnsureReadBuffer(int size)
        {
            if (_readBuffer.Length < size)
                _readBuffer = new byte[Math.Max(size, _readBuffer.Length * 2)];
        }

        private void TrimPooledBuffers()
        {
            // Keep steady-state buffers; release one-off large ones (e.g. a big legacy file).
            if (_readBuffer.Length > MaxPooledBufferBytes)
                _readBuffer = new byte[16 * 1024];
            if (_bodyBuffer.Capacity > MaxPooledBufferBytes)
            {
                _bodyBuffer.SetLength(0);
                _bodyBuffer.Capacity = 16 * 1024;
            }
            if (_gzipBuffer.Capacity > MaxPooledBufferBytes)
            {
                _gzipBuffer.SetLength(0);
                _gzipBuffer.Capacity = 8 * 1024;
            }
        }

        private static bool LooksLikeObject(byte[] data, int length)
        {
            int i = 0;
            while (i < length && data[i] <= (byte)' ')
                i++;
            return i < length && data[i] == (byte)'{';
        }

        /// <summary>
        /// Wraps complete JSON lines into {"events":[...]}. Exposed for tests.
        /// </summary>
        internal static byte[] BuildBatchBody(byte[] raw, out int eventCount)
        {
            eventCount = 0;
            using (var output = new MemoryStream(raw.Length + BatchPrefix.Length + BatchSuffix.Length))
            {
                output.Write(BatchPrefix, 0, BatchPrefix.Length);
                int lineStart = 0;
                for (int i = 0; i < raw.Length; i++)
                {
                    if (raw[i] != (byte)'\n')
                        continue;
                    int start = lineStart;
                    int end = i;
                    lineStart = i + 1;
                    if (end > start && raw[end - 1] == (byte)'\r')
                        end--;
                    if (!JsonValidator.IsObject(raw, start, end))
                        continue;
                    if (eventCount > 0)
                        output.WriteByte((byte)',');
                    output.Write(raw, start, end - start);
                    eventCount++;
                }
                output.Write(BatchSuffix, 0, BatchSuffix.Length);
                return eventCount > 0 ? output.ToArray() : null;
            }
        }

        private static int CountOccurrences(byte[] data, int length, byte[] pattern)
        {
            int count = 0;
            for (int i = IndexOf(data, 0, length, pattern); i >= 0; i = IndexOf(data, i + pattern.Length, length, pattern))
                count++;
            return count;
        }

        private static int IndexOf(byte[] data, int start, int length, byte[] pattern)
        {
            for (int i = start; i <= length - pattern.Length; i++)
            {
                int j = 0;
                while (j < pattern.Length && data[i + j] == pattern[j])
                    j++;
                if (j == pattern.Length)
                    return i;
            }
            return -1;
        }
    }
}
