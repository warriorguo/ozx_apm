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
    /// A batch read from local storage and ready to upload.
    /// </summary>
    public sealed class UploadBatch
    {
        /// <summary>Identifies the stored segment; pass the batch back to CompleteUpload.</summary>
        public string SegmentName;
        /// <summary>Request body, gzip-compressed when Compressed is true.</summary>
        public byte[] Payload;
        public bool Compressed;
        public int EventCount;
        public int UncompressedBytes;
        /// <summary>Uncompressed JSON for the network log; only filled when LogLevel is Debug.</summary>
        public string DebugJson;
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
    /// Layout: one directory of segment files. Each segment holds up to
    /// BatchSize events / MaxBatchBytes as JSON Lines (one event per line, the
    /// exact JSON that is uploaded), and each segment is one upload request.
    ///   NNNNNNNNNNNNN-NNNNNN.open  segment currently being appended to
    ///   NNNNNNNNNNNNN-NNNNNN.seg   sealed segment, waiting for upload
    ///   seq                        reserved high-water mark of the event sequence
    ///   ozx_apm_offline_*.json     legacy (pre-APM-2) batch files, uploaded as-is
    /// A segment is sealed when it is full, older than FlushIntervalSeconds, on
    /// Flush() and at startup (".open" files left by a crash or kill).
    ///
    /// Threading: all file I/O happens on one background worker thread. The
    /// main thread only appends to an in-memory list under a lock. Pending
    /// events are written at least every PersistIntervalMs, and immediately for
    /// exceptions and crashes, so a crash or kill loses at most that window.
    /// Uploads never re-parse events: lines are concatenated into the request
    /// body, so reading old files does not depend on culture or JsonUtility.
    ///
    /// Nothing is written (no file, no directory) unless consent is Granted.
    /// </summary>
    public class OfflineStorage
    {
        private const string SegmentExtension = ".seg";
        private const string OpenExtension = ".open";
        private const string SeqFileName = "seq";
        private const string LegacyFilePrefix = "ozx_apm_offline_";
        private const string LegacyFileExtension = ".json";
        private const long SeqReserveBlock = 1000;
        private const int CompressThresholdBytes = 1024;

        private static readonly byte[] BatchPrefix = Encoding.UTF8.GetBytes("{\"events\":[");
        private static readonly byte[] BatchSuffix = Encoding.UTF8.GetBytes("]}");
        private static readonly byte[] LegacyEventMarker = Encoding.UTF8.GetBytes("{\"type\":\"");

        private readonly ApmConfig _config;
        private readonly string _storagePath;
        private readonly bool _persistToDisk;
        private readonly bool _useWorkerThread;

        // ---- Shared between main thread and worker; guarded by _lock ----
        private readonly object _lock = new object();
        private List<BaseEvent> _pending = new List<BaseEvent>(64);
        private bool _flushRequested;
        private bool _clearRequested;
        private bool _uploadRequested;
        private UploadPoll _uploadState = UploadPoll.Empty;
        private UploadBatch _preparedUpload;
        private readonly List<string> _segmentsToDelete = new List<string>();
        private bool _releaseInFlight;
        private bool _stopRequested;
        private long _droppedPendingEvents;

        // ---- Worker-only state ----
        private readonly AutoResetEvent _wake = new AutoResetEvent(false);
        private readonly ManualResetEvent _idleSignal = new ManualResetEvent(true);
        private bool _isIdle = true; // mirrors _idleSignal; guarded by _lock
        private Thread _thread;
        private List<BaseEvent> _drain = new List<BaseEvent>(64);
        private readonly List<string> _deleteDrain = new List<string>();
        private readonly List<Segment> _segments = new List<Segment>(); // sealed, oldest first
        private Segment _open;
        private FileStream _openStream;
        private readonly MemoryStream _lineBuffer = new MemoryStream(16 * 1024);
        private string _inFlight;
        private long _totalBytes;
        private long _seq;
        private long _seqReserved;
        private int _segmentCounter;
        private bool _loaded;
        private bool _directoryReady;
        private long _droppedByCap;
        private int _lastSyncPumpTicks;

        private sealed class Segment
        {
            public string Name;
            public string Path;      // null in memory-only mode
            public MemoryStream Mem; // memory-only mode
            public long Bytes;
            public int EventCount;
            public bool Legacy;
            public DateTime CreatedUtc;
        }

        public OfflineStorage(ApmConfig config)
            : this(config,
                   Path.Combine(Application.persistentDataPath, "ozx_apm_offline"),
                   Application.platform != RuntimePlatform.WebGLPlayer)
        {
        }

        /// <param name="storagePath">Directory for segment files.</param>
        /// <param name="useWorkerThread">
        /// false: the caller drives the worker with Pump() (tests, platforms without threads).
        /// </param>
        public OfflineStorage(ApmConfig config, string storagePath, bool useWorkerThread)
        {
            _config = config;
            _storagePath = storagePath;
            _persistToDisk = config.EnableOfflineStorage;
            _useWorkerThread = useWorkerThread;
        }

        public string StoragePath => _storagePath;

        /// <summary>Bytes currently held in local storage (worker's view).</summary>
        public long GetStorageSize() => Interlocked.Read(ref _totalBytes);

        /// <summary>Events dropped because the in-memory hand-off or the storage cap overflowed.</summary>
        public long DroppedEventCount => Interlocked.Read(ref _droppedPendingEvents) + Interlocked.Read(ref _droppedByCap);

        /// <summary>
        /// Starts the background worker. Does no I/O on the calling thread:
        /// scanning old files happens on the worker.
        /// </summary>
        public void Start()
        {
            if (!_useWorkerThread || _thread != null)
                return;

            _thread = new Thread(WorkerLoop)
            {
                IsBackground = true,
                Name = "OzxApm.Storage",
                Priority = System.Threading.ThreadPriority.BelowNormal
            };
            _thread.Start();
        }

        /// <summary>
        /// Queues an event for persistence. Main thread; O(1), no I/O.
        /// </summary>
        public void Enqueue(BaseEvent evt)
        {
            if (evt == null || _config.Consent != ConsentState.Granted)
                return;

            bool urgent = evt is ExceptionEvent || evt is CrashEvent;
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
                _pending.Add(evt);
                MarkBusy();
            }

            if (urgent)
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
            if (!_useWorkerThread || _thread == null)
            {
                Pump();
                return true;
            }
            return _idleSignal.WaitOne(timeoutMs);
        }

        /// <summary>
        /// Deletes all locally stored events (async). Entry point for APM-14 (consent revoked).
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
        /// Asks the worker to prepare the oldest sealed segment for upload.
        /// Poll the result with PollUpload. Main thread.
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
            if (!_useWorkerThread)
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
        /// Reports the outcome of an upload. delete=true only when the server
        /// confirmed the batch (or rejected it as permanently invalid); otherwise
        /// the segment stays on disk and is retried.
        /// </summary>
        public void CompleteUpload(UploadBatch batch, bool delete)
        {
            if (batch == null)
                return;

            lock (_lock)
            {
                if (delete)
                    _segmentsToDelete.Add(batch.SegmentName);
                _releaseInFlight = true;
            }
            _wake.Set();
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

            if (_thread != null)
            {
                try { _thread.Join(timeoutMs); } catch { }
                _thread = null;
            }
            else
            {
                Pump();
            }
        }

        /// <summary>
        /// Call once per frame from the main thread. Only does work on platforms
        /// without a worker thread, where it pumps the worker every PersistIntervalMs.
        /// </summary>
        public void Tick()
        {
            if (_useWorkerThread)
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
                ApmClient.Log(LogLevel.Warning, $"Storage worker error: {ex.GetType().Name}: {ex.Message}");
            }
        }

        // Called under _lock. Only touches the wait handle on a state change, so a
        // steady stream of Enqueue calls costs no kernel call per event.
        private void MarkBusy()
        {
            if (!_isIdle)
                return;
            _isIdle = false;
            _idleSignal.Reset();
        }

        private void MarkIdle()
        {
            if (_isIdle)
                return;
            _isIdle = true;
            _idleSignal.Set();
        }

        private void WorkerLoop()
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

                _wake.WaitOne(Math.Max(10, _config.PersistIntervalMs));
            }

            try { CloseOpenStream(); } catch { }
            lock (_lock)
            {
                MarkIdle();
            }
        }

        private void PumpInternal()
        {
            if (!_loaded)
            {
                _loaded = true; // once, even if the scan fails
                LoadExisting();
            }

            bool flush, clear, upload, release;
            lock (_lock)
            {
                var swap = _drain;
                _drain = _pending;
                _pending = swap;

                flush = _flushRequested;
                clear = _clearRequested;
                upload = _uploadRequested;
                release = _releaseInFlight;
                _flushRequested = false;
                _clearRequested = false;
                _uploadRequested = false;
                _releaseInFlight = false;

                _deleteDrain.Clear();
                _deleteDrain.AddRange(_segmentsToDelete);
                _segmentsToDelete.Clear();
            }

            try
            {
                for (int i = 0; i < _deleteDrain.Count; i++)
                    DeleteSegment(_deleteDrain[i]);
                if (release)
                    _inFlight = null;

                if (clear)
                    DeleteAll();

                if (_drain.Count > 0)
                    WriteEvents(_drain);

                if (_open != null && _open.EventCount > 0 &&
                    (flush || (DateTime.UtcNow - _open.CreatedUtc).TotalSeconds >= _config.FlushIntervalSeconds))
                {
                    SealOpen();
                }

                EnforceCap();

                if (upload)
                    PrepareUpload();
            }
            finally
            {
                _drain.Clear();
                lock (_lock)
                {
                    if (_pending.Count == 0 && !_flushRequested && !_clearRequested && _segmentsToDelete.Count == 0)
                    {
                        MarkIdle();
                    }
                }
            }
        }

        // ------------------------------------------------------------------
        // Startup scan (worker thread)
        // ------------------------------------------------------------------

        private void LoadExisting()
        {
            if (!_persistToDisk || !Directory.Exists(_storagePath))
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
                            AddSegment(sealedPath, false);
                        }
                        catch (IOException)
                        {
                            AddSegment(file, false);
                        }
                    }
                    else if (name.EndsWith(SegmentExtension, StringComparison.Ordinal))
                    {
                        AddSegment(file, false);
                    }
                    else if (name.StartsWith(LegacyFilePrefix, StringComparison.Ordinal) &&
                             name.EndsWith(LegacyFileExtension, StringComparison.Ordinal))
                    {
                        AddSegment(file, true);
                    }
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Warning, $"Skipping unreadable event file {name}: {ex.Message}");
                }
            }

            // Legacy files sort after numbered segments by name; they are older, so upload them first.
            _segments.Sort((a, b) =>
            {
                if (a.Legacy != b.Legacy) return a.Legacy ? -1 : 1;
                return string.CompareOrdinal(a.Name, b.Name);
            });

            _seq = ReadSeqHighWater();
            _seqReserved = _seq;

            if (_segments.Count > 0)
                ApmClient.Log(LogLevel.Info, $"Found {_segments.Count} stored event batches ({_totalBytes} bytes) to upload");
        }

        private void AddSegment(string path, bool legacy)
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
                Legacy = legacy,
                EventCount = -1
            });
            _totalBytes += bytes;
        }

        private long ReadSeqHighWater()
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
                    // Unreadable: fall back to what we have.
                }
            }
            return best;
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

            ReserveSeq(events.Count);

            for (int i = 0; i < events.Count; i++)
            {
                var evt = events[i];
                evt.seq = ++_seq;

                string line;
                try
                {
                    line = JsonSerializer.SerializeEvent(evt);
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Warning, $"Failed to serialize {evt.type} event: {ex.Message}");
                    continue;
                }

                int lineBytes = Encoding.UTF8.GetByteCount(line) + 1;
                if (_open != null && _open.EventCount > 0 &&
                    _open.Bytes + _lineBuffer.Length + lineBytes > maxBytes)
                {
                    FlushLineBuffer();
                    SealOpen();
                }

                if (_open == null)
                    OpenSegment();

                var bytes = Encoding.UTF8.GetBytes(line);
                _lineBuffer.Write(bytes, 0, bytes.Length);
                _lineBuffer.WriteByte((byte)'\n');
                _open.EventCount++;

                if (_open.EventCount >= batchSize)
                {
                    FlushLineBuffer();
                    SealOpen();
                }
            }

            FlushLineBuffer();
        }

        private void ReserveSeq(int count)
        {
            if (_seq + count <= _seqReserved)
                return;

            _seqReserved = _seq + count + SeqReserveBlock;
            if (!_persistToDisk)
                return;

            // Persist the reservation before any event uses it, so a crash can
            // only create gaps, never reuse a sequence number.
            try
            {
                EnsureDirectory();
                string path = Path.Combine(_storagePath, SeqFileName);
                string tmp = path + ".tmp";
                File.WriteAllText(tmp, _seqReserved.ToString(CultureInfo.InvariantCulture));
                if (File.Exists(path))
                    File.Delete(path);
                File.Move(tmp, path);
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to persist event sequence: {ex.Message}");
            }
        }

        private void OpenSegment()
        {
            var now = DateTime.UtcNow;
            long ms = new DateTimeOffset(now).ToUnixTimeMilliseconds();
            string baseName = SegmentBaseName(ms);

            _open = new Segment { CreatedUtc = now };

            if (_persistToDisk)
            {
                EnsureDirectory();
                // Names sort by creation time; never reuse a name left by a previous launch.
                while (File.Exists(Path.Combine(_storagePath, baseName + SegmentExtension)) ||
                       File.Exists(Path.Combine(_storagePath, baseName + OpenExtension)))
                {
                    baseName = SegmentBaseName(ms);
                }
                _open.Name = baseName + SegmentExtension;
                _open.Path = Path.Combine(_storagePath, baseName + OpenExtension);
                _openStream = new FileStream(_open.Path, FileMode.Append, FileAccess.Write, FileShare.Read, 4096);
            }
            else
            {
                _open.Name = baseName + SegmentExtension;
                _open.Mem = new MemoryStream();
            }
        }

        private string SegmentBaseName(long unixMs)
        {
            return unixMs.ToString("D13", CultureInfo.InvariantCulture) + "-" +
                   (_segmentCounter++ % 1000000).ToString("D6", CultureInfo.InvariantCulture);
        }

        private void FlushLineBuffer()
        {
            if (_lineBuffer.Length == 0 || _open == null)
                return;

            int length = (int)_lineBuffer.Length;
            try
            {
                if (_open.Mem != null)
                {
                    _open.Mem.Write(_lineBuffer.GetBuffer(), 0, length);
                }
                else
                {
                    _openStream.Write(_lineBuffer.GetBuffer(), 0, length);
                    // Hand the bytes to the OS so they survive the process being killed.
                    _openStream.Flush();
                }
                _open.Bytes += length;
                _totalBytes += length;
            }
            finally
            {
                _lineBuffer.SetLength(0);
            }
        }

        private void SealOpen()
        {
            if (_open == null)
                return;

            var segment = _open;
            _open = null;
            CloseOpenStream();

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

            _segments.Add(segment);
        }

        private void CloseOpenStream()
        {
            if (_openStream == null)
                return;
            try { _openStream.Dispose(); } catch { }
            _openStream = null;
        }

        private void EnsureDirectory()
        {
            if (_directoryReady)
                return;
            Directory.CreateDirectory(_storagePath);
            _directoryReady = true;
        }

        // ------------------------------------------------------------------
        // Cap and deletion (worker thread)
        // ------------------------------------------------------------------

        private void EnforceCap()
        {
            long cap = Math.Max(1024, (long)_config.MaxOfflineStorageBytes);
            while (_totalBytes > cap && _segments.Count > 0)
            {
                var oldest = _segments[0];
                _segments.RemoveAt(0);
                if (oldest.EventCount > 0)
                    _droppedByCap += oldest.EventCount;
                DeleteSegmentData(oldest);
                ApmClient.Log(LogLevel.Debug, $"Storage full, dropped oldest batch {oldest.Name}");
            }
        }

        private void DeleteSegment(string name)
        {
            for (int i = 0; i < _segments.Count; i++)
            {
                if (_segments[i].Name == name)
                {
                    var segment = _segments[i];
                    _segments.RemoveAt(i);
                    DeleteSegmentData(segment);
                    return;
                }
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
                    ApmClient.Log(LogLevel.Warning, $"Failed to delete {segment.Name}: {ex.Message}");
                }
            }
            segment.Mem = null;
        }

        private void DeleteAll()
        {
            CloseOpenStream();
            if (_open != null)
            {
                DeleteSegmentData(_open);
                _open = null;
            }
            for (int i = 0; i < _segments.Count; i++)
                DeleteSegmentData(_segments[i]);
            _segments.Clear();
            _inFlight = null;
            _totalBytes = 0;

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

            if (_config.Consent == ConsentState.Granted && _inFlight == null)
            {
                while (_segments.Count > 0 && batch == null)
                {
                    var segment = _segments[0];
                    batch = BuildBatch(segment);
                    if (batch == null)
                    {
                        // Nothing valid inside (e.g. only a torn line): drop it.
                        _segments.RemoveAt(0);
                        DeleteSegmentData(segment);
                    }
                }
            }

            if (batch != null)
                _inFlight = batch.SegmentName;

            lock (_lock)
            {
                _preparedUpload = batch;
                _uploadState = batch != null ? UploadPoll.Ready : UploadPoll.Empty;
            }
        }

        private UploadBatch BuildBatch(Segment segment)
        {
            byte[] raw;
            try
            {
                raw = segment.Mem != null ? segment.Mem.ToArray() : File.ReadAllBytes(segment.Path);
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to read {segment.Name}: {ex.Message}");
                return null;
            }

            byte[] body;
            int eventCount;
            if (segment.Legacy)
            {
                body = BuildLegacyBody(raw, out eventCount);
            }
            else
            {
                body = BuildBatchBody(raw, out eventCount);
            }

            if (body == null || eventCount == 0)
                return null;

            var batch = new UploadBatch
            {
                SegmentName = segment.Name,
                EventCount = eventCount,
                UncompressedBytes = body.Length,
                Payload = body
            };

            if (_config.LogLevel >= LogLevel.Debug)
                batch.DebugJson = Encoding.UTF8.GetString(body);

            if (_config.EnableCompression && body.Length > CompressThresholdBytes)
            {
                var compressed = Gzip(body);
                if (compressed != null)
                {
                    batch.Payload = compressed;
                    batch.Compressed = true;
                }
            }

            return batch;
        }

        /// <summary>
        /// Wraps complete JSON lines into {"events":[...]}. A last line without a
        /// trailing newline was cut off by a crash and is skipped, as is any line
        /// that is not a JSON object.
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
                    int end = i; // exclusive
                    lineStart = i + 1;

                    if (end > start && raw[end - 1] == (byte)'\r')
                        end--;
                    if (end - start < 2 || raw[start] != (byte)'{' || raw[end - 1] != (byte)'}')
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

        /// <summary>
        /// Pre-APM-2 files already contain a full {"events":[...]} body; send them
        /// unchanged. If the old serializer wrote invalid JSON (e.g. "59,50" under
        /// de-DE) the server answers 400 and the file is dropped.
        /// </summary>
        private static byte[] BuildLegacyBody(byte[] raw, out int eventCount)
        {
            eventCount = 0;
            int first = 0;
            while (first < raw.Length && raw[first] <= (byte)' ')
                first++;
            if (first >= raw.Length || raw[first] != (byte)'{')
                return null;

            eventCount = CountOccurrences(raw, LegacyEventMarker);
            if (eventCount == 0)
                eventCount = 1; // unknown, but non-empty
            return raw;
        }

        private static int CountOccurrences(byte[] data, byte[] pattern)
        {
            int count = 0;
            for (int i = 0; i <= data.Length - pattern.Length; i++)
            {
                int j = 0;
                while (j < pattern.Length && data[i + j] == pattern[j])
                    j++;
                if (j == pattern.Length)
                {
                    count++;
                    i += pattern.Length - 1;
                }
            }
            return count;
        }

        private static byte[] Gzip(byte[] data)
        {
            try
            {
                using (var output = new MemoryStream())
                {
                    using (var gzip = new GZipStream(output, CompressionMode.Compress))
                    {
                        gzip.Write(data, 0, data.Length);
                    }
                    return output.ToArray();
                }
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Compression failed: {ex.Message}");
                return null;
            }
        }
    }

    /// <summary>
    /// Records all network communication with the server for debugging purposes.
    /// Logs are persisted to file and can be retrieved for analysis.
    /// </summary>
    public class NetworkLogger
    {
        private readonly ApmConfig _config;
        private readonly string _logFilePath;
        private readonly object _lock = new object();
        private readonly List<NetworkLogEntry> _recentLogs = new List<NetworkLogEntry>();

        private const int MaxRecentLogs = 100;
        private const string LogFileName = "ozx_apm_network.log";

        public NetworkLogger(ApmConfig config)
        {
            _config = config;
            _logFilePath = Path.Combine(Application.persistentDataPath, LogFileName);

            // Output log file path to console
            Debug.Log($"[OzxApm] Network log file: {_logFilePath}");

            // Write session start marker
            WriteLog(new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Message = $"=== Network logging session started === App: {config.AppVersion}, Server: {config.ServerUrl}"
            });
        }

        /// <summary>
        /// Logs a request being sent to the server
        /// </summary>
        public void LogRequest(string url, string method, Dictionary<string, string> headers, string body, int bodyBytes, bool isCompressed)
        {
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.Request,
                Url = url,
                Method = method,
                Headers = headers,
                Body = TruncateBody(body),
                BodyBytes = bodyBytes,
                IsCompressed = isCompressed,
                Message = $"REQUEST: {method} {url} ({bodyBytes} bytes{(isCompressed ? ", gzip" : "")})"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs a successful response from the server
        /// </summary>
        public void LogResponse(string url, int statusCode, string responseBody, double elapsedMs, int eventCount)
        {
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.Response,
                Url = url,
                StatusCode = statusCode,
                Body = TruncateBody(responseBody),
                ElapsedMs = elapsedMs,
                EventCount = eventCount,
                Message = $"RESPONSE: {statusCode} OK from {url} ({elapsedMs:F0}ms, {eventCount} events)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs a failed request
        /// </summary>
        public void LogFailure(string url, int statusCode, string error, string responseBody, double elapsedMs, int eventCount, int consecutiveFailures, float backoffMultiplier)
        {
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Error,
                Type = NetworkLogType.Failure,
                Url = url,
                StatusCode = statusCode,
                Error = error,
                Body = TruncateBody(responseBody),
                ElapsedMs = elapsedMs,
                EventCount = eventCount,
                ConsecutiveFailures = consecutiveFailures,
                BackoffMultiplier = backoffMultiplier,
                Message = $"FAILURE: {error} from {url} (status: {statusCode}, {elapsedMs:F0}ms, failures: {consecutiveFailures}, backoff: {backoffMultiplier}x)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs when events are queued for offline storage
        /// </summary>
        public void LogOfflineQueue(int eventCount, string reason)
        {
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Warning,
                Type = NetworkLogType.OfflineQueue,
                EventCount = eventCount,
                Message = $"OFFLINE_QUEUE: {eventCount} events queued - {reason}"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs when offline events are being retried
        /// </summary>
        public void LogOfflineRetry(int fileCount, int eventCount)
        {
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.OfflineRetry,
                EventCount = eventCount,
                Message = $"OFFLINE_RETRY: Processing {fileCount} offline files ({eventCount} events)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs compression details
        /// </summary>
        public void LogCompression(int originalBytes, int compressedBytes)
        {
            float ratio = (float)compressedBytes / originalBytes;
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Debug,
                Type = NetworkLogType.Compression,
                BodyBytes = compressedBytes,
                Message = $"COMPRESSION: {originalBytes} -> {compressedBytes} bytes ({ratio:P0})"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Gets recent log entries (in-memory cache)
        /// </summary>
        public List<NetworkLogEntry> GetRecentLogs()
        {
            lock (_lock)
            {
                return new List<NetworkLogEntry>(_recentLogs);
            }
        }

        /// <summary>
        /// Gets the full log file path
        /// </summary>
        public string GetLogFilePath()
        {
            return _logFilePath;
        }

        /// <summary>
        /// Clears the log file
        /// </summary>
        public void ClearLogs()
        {
            lock (_lock)
            {
                _recentLogs.Clear();
                try
                {
                    if (File.Exists(_logFilePath))
                    {
                        File.Delete(_logFilePath);
                    }
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Warning, $"Failed to clear network log file: {ex.Message}");
                }
            }
        }

        private void WriteLog(NetworkLogEntry entry)
        {
            lock (_lock)
            {
                // Add to recent logs cache
                _recentLogs.Add(entry);
                if (_recentLogs.Count > MaxRecentLogs)
                {
                    _recentLogs.RemoveAt(0);
                }

                // Only write to file in debug builds or if log level is Debug
                if (!Debug.isDebugBuild && _config.LogLevel > LogLevel.Debug)
                    return;

                // The log contains request bodies: no file without consent.
                if (_config.Consent != ConsentState.Granted)
                    return;

                try
                {
                    string logLine = FormatLogEntry(entry);
                    File.AppendAllText(_logFilePath, logLine + Environment.NewLine);
                }
                catch (Exception ex)
                {
                    // Silently fail - don't want logging to break the SDK
                    Debug.LogWarning($"[OzxApm] Failed to write network log: {ex.Message}");
                }
            }
        }

        private void LogToConsole(NetworkLogEntry entry)
        {
            if (_config.LogLevel > LogLevel.Debug)
                return;

            // Never Debug.LogError: upload failures are expected (offline players)
            // and an error-level log would be captured by ExceptionCollector and
            // by the game's own error reporting.
            string prefix = "[OzxApm] [Network] ";
            switch (entry.Level)
            {
                case NetworkLogLevel.Error:
                case NetworkLogLevel.Warning:
                    Debug.LogWarning(prefix + entry.Message);
                    break;
                default:
                    Debug.Log(prefix + entry.Message);
                    break;
            }
        }

        private string FormatLogEntry(NetworkLogEntry entry)
        {
            var sb = new StringBuilder();
            sb.Append($"[{entry.Timestamp:yyyy-MM-dd HH:mm:ss.fff}] ");
            sb.Append($"[{entry.Level}] ");
            sb.Append(entry.Message);

            // Add detailed info for requests and failures
            if (entry.Type == NetworkLogType.Request && entry.Headers != null)
            {
                sb.Append(" | Headers: ");
                foreach (var kv in entry.Headers)
                {
                    // Mask sensitive headers
                    string value = kv.Key.ToLower().Contains("key") || kv.Key.ToLower().Contains("auth")
                        ? MaskValue(kv.Value)
                        : kv.Value;
                    sb.Append($"{kv.Key}={value}, ");
                }
            }

            if (!string.IsNullOrEmpty(entry.Body) && (entry.Type == NetworkLogType.Request || entry.Type == NetworkLogType.Failure))
            {
                sb.Append($" | Body: {entry.Body}");
            }

            if (!string.IsNullOrEmpty(entry.Error))
            {
                sb.Append($" | Error: {entry.Error}");
            }

            return sb.ToString();
        }

        private string TruncateBody(string body)
        {
            if (string.IsNullOrEmpty(body))
                return null;

            const int maxLength = 1000;
            return body.Length > maxLength
                ? body.Substring(0, maxLength) + "... (truncated)"
                : body;
        }

        private string MaskValue(string value)
        {
            if (string.IsNullOrEmpty(value) || value.Length <= 4)
                return "****";
            return value.Substring(0, 4) + "****";
        }
    }

    /// <summary>
    /// Represents a single network log entry
    /// </summary>
    [Serializable]
    public class NetworkLogEntry
    {
        public DateTime Timestamp;
        public NetworkLogLevel Level;
        public NetworkLogType Type;
        public string Message;
        public string Url;
        public string Method;
        public Dictionary<string, string> Headers;
        public string Body;
        public int BodyBytes;
        public bool IsCompressed;
        public int StatusCode;
        public string Error;
        public double ElapsedMs;
        public int EventCount;
        public int ConsecutiveFailures;
        public float BackoffMultiplier;
    }

    public enum NetworkLogLevel
    {
        Debug,
        Info,
        Warning,
        Error
    }

    public enum NetworkLogType
    {
        Request,
        Response,
        Failure,
        OfflineQueue,
        OfflineRetry,
        Compression,
        Other
    }
}
