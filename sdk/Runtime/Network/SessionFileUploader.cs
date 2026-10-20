using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Network
{
    /// <summary>The state of one session file upload (ApmSessionFiles.GetUploads).</summary>
    public sealed class SessionUploadInfo
    {
        public string UploadId;
        public string Directory;
        public string Recording;
        public SessionUploadState State;
        public long SizeBytes;
        public int PartsSent;
        public int PartCount;
        public string Error;

        public override string ToString() =>
            $"{UploadId} {Recording} {State} {PartsSent}/{PartCount} parts, {SizeBytes} bytes{(Error != null ? " (" + Error + ")" : "")}";
    }

    /// <summary>What Upload captures on the calling thread (device and build facts).</summary>
    internal struct SessionUploadFacts
    {
        public string DeviceId, SessionId, CurrentSessionId, GitCommit, AppVersion, Build, BuildType, Platform, DeviceModel, OsVersion, UnityVersion;
    }

    /// <summary>
    /// The session file uploader (APM-13): a queue of its own, apart from the
    /// event and ops queues, on its own background thread (below normal
    /// priority). Per upload: zip the directory (streamed, SHA-256 while writing),
    /// create the upload on the server, send the parts one at a time from a part
    /// file, commit. The state is persisted after every step
    /// (<see cref="SessionFileRecord"/>), so an upload resumes where it stopped
    /// after the app was killed or went offline; the server tells which parts it
    /// has. Requests go through <see cref="SessionFileHttp"/> (UnityWebRequest on
    /// the main thread, started only while the event and ops uploaders are not
    /// sending), paced to <see cref="ApmConfig.SessionFilesBytesPerSecond"/>, only
    /// with technical consent, and (by default) only on Wi-Fi.
    ///
    /// Failures are retried with backoff (5 s doubling to an hour, Retry-After
    /// honoured) for as long as the recording is younger than
    /// <see cref="ApmConfig.SessionFileMaxAgeDays"/>; only an explicit
    /// <c>reject_code</c> from the server drops an upload. The local copy (zip,
    /// part, state) is deleted when the server has it, when it is dropped, when
    /// technical consent is denied and when the device id is reset. The
    /// recording directory itself is the game's and never touched.
    /// </summary>
    internal sealed class SessionFileUploader
    {
        internal const string DirectoryName = "ozx_apm_sessionfiles";
        private const int MaxSessionJsonBytes = 64 * 1024;
        private const int MaxPackAttempts = 3;
        private const int MaxFinished = 20;

        private readonly ApmConfig _config;
        private readonly string _root;
        private readonly SessionFileHttp _http;
        private readonly Func<bool> _allowed;
        private readonly string _url;
        private readonly System.Random _random = new System.Random();

        private readonly object _lock = new object();
        private readonly List<SessionFileRecord> _incoming = new List<SessionFileRecord>();
        private readonly List<SessionFileRecord> _records = new List<SessionFileRecord>(); // worker
        private readonly List<SessionUploadInfo> _finished = new List<SessionUploadInfo>();
        private SessionUploadInfo[] _snapshot = new SessionUploadInfo[0];
        private readonly AutoResetEvent _wake = new AutoResetEvent(false);
        private Thread _thread;
        private volatile bool _stop;
        private volatile bool _clearRequested;
        private volatile bool _paused;
        private volatile bool _hasWork;
        private volatile int _reachability = (int)NetworkReachability.ReachableViaLocalAreaNetwork;
        private float _nextReachabilityCheck;
        private readonly Dictionary<string, long> _notBefore = new Dictionary<string, long>(); // worker
        private readonly HashSet<string> _synced = new HashSet<string>(); // worker: created in this process
        private int _failures;
        private byte[] _buffer;

        // Stats (any thread).
        private long _bytesSent;
        private int _completed;
        private int _failed;
        private volatile string _lastError;

        /// <summary>Milliseconds clock (tests replace it).</summary>
        internal Func<long> NowMs = () => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
        /// <summary>Called instead of sleeping between parts (tests).</summary>
        internal Action<int> Pace;
        /// <summary>Called on the worker after every step (tests).</summary>
        internal Action AfterStep;
        /// <summary>The network state (main thread; tests replace it).</summary>
        internal Func<NetworkReachability> Reachability = () => Application.internetReachability;

        public SessionFileUploader(ApmConfig config, string root, SessionFileHttp http, Func<bool> allowed)
        {
            _config = config;
            _root = root;
            _http = http;
            _allowed = allowed;
            _url = (config.ServerUrl ?? "").TrimEnd('/') + "/v1/session-files";
        }

        public string Root => _root;
        public long BytesSent => Interlocked.Read(ref _bytesSent);
        public int Completed => _completed;
        public int Failed => _failed;
        public string LastError => _lastError;
        public bool HasWorkerThread => _thread != null;

        /// <summary>Uploads waiting or under way.</summary>
        public int Pending => Volatile.Read(ref _pendingCount);
        private int _pendingCount;

        /// <summary>Wakes the worker (tests: after moving the clock).</summary>
        internal void WakeForTests() => _wake.Set();

        public void Start()
        {
            if (_thread != null)
                return;
            _thread = new Thread(Run) { IsBackground = true, Name = "OzxApm.SessionFiles" };
            try
            {
                _thread.Priority = System.Threading.ThreadPriority.BelowNormal;
            }
            catch (Exception)
            {
                // Not supported on every runtime: normal priority then.
            }
            _thread.Start();
        }

        /// <summary>Any thread. Queues a directory; returns its upload id, or null when refused.</summary>
        public string Enqueue(string directory, SessionUploadFacts facts)
        {
            string full;
            try
            {
                full = Path.GetFullPath(directory).TrimEnd('/', '\\');
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Session files: invalid directory {directory}: {ex.Message}");
                return null;
            }
            string name = Path.GetFileName(full);
            if (!IsValidRecordingName(name))
            {
                ApmClient.Log(LogLevel.Warning, $"Session files: '{name}' cannot be a session directory name (A-Z a-z 0-9 _ . -, 1-128, not starting with . or -)");
                return null;
            }
            var r = new SessionFileRecord
            {
                Id = Guid.NewGuid().ToString("N"),
                Directory = full,
                Recording = name,
                State = SessionUploadState.Queued,
                CreatedAtMs = NowMs(),
                DeviceId = facts.DeviceId,
                SessionId = facts.SessionId,
                CurrentSession = facts.CurrentSessionId,
                GitCommit = facts.GitCommit,
                AppVersion = facts.AppVersion,
                Build = facts.Build,
                BuildType = facts.BuildType,
                Platform = facts.Platform,
                DeviceModel = facts.DeviceModel,
                OsVersion = facts.OsVersion,
                UnityVersion = facts.UnityVersion,
                PartBytes = ClampPart(_config.SessionFilePartBytes)
            };
            if (Interlocked.Increment(ref _pendingCount) > Math.Max(1, _config.MaxPendingSessionFiles))
            {
                Interlocked.Decrement(ref _pendingCount);
                ApmClient.Log(LogLevel.Warning, $"Session files: {_config.MaxPendingSessionFiles} uploads are pending already; {directory} not queued");
                return null;
            }
            lock (_lock)
                _incoming.Add(r);
            _hasWork = true;
            _wake.Set();
            return r.Id;
        }

        internal static bool IsValidRecordingName(string name)
        {
            if (string.IsNullOrEmpty(name) || name.Length > 128 || name.Contains(".."))
                return false;
            for (int i = 0; i < name.Length; i++)
            {
                char c = name[i];
                bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (i > 0 && (c == '_' || c == '.' || c == '-'));
                if (!ok)
                    return false;
            }
            return true;
        }

        /// <summary>An id the server accepts: 1-128 of A-Z a-z 0-9 _ . : -.</summary>
        internal static bool IsValidId(string id)
        {
            if (string.IsNullOrEmpty(id) || id.Length > 128)
                return false;
            foreach (char c in id)
            {
                if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == ':' || c == '-'))
                    return false;
            }
            return true;
        }

        private static int ClampPart(int bytes) => Math.Max(64 * 1024, Math.Min(4 * 1024 * 1024, bytes));

        /// <summary>Copies of the uploads' states (allocates; diagnostics).</summary>
        public SessionUploadInfo[] GetUploads()
        {
            var s = _snapshot;
            var copy = new SessionUploadInfo[s.Length];
            Array.Copy(s, copy, s.Length);
            return copy;
        }

        /// <summary>Main thread, every frame. No allocation.</summary>
        public void Update(float now, bool othersSending)
        {
            if (_hasWork && now >= _nextReachabilityCheck)
            {
                _nextReachabilityCheck = now + 1f;
                _reachability = (int)Reachability();
            }
            _http.Update(now, othersSending);
        }

        /// <summary>Main thread: the app is paused / resumed.</summary>
        public void OnPause(bool paused)
        {
            _paused = paused;
            if (!paused)
            {
                _failures = 0;
                _wake.Set();
            }
        }

        /// <summary>Technical consent changed: Granted resumes; otherwise the request under way is aborted.</summary>
        public void OnConsent(ConsentState state)
        {
            if (state == ConsentState.Granted)
            {
                _http.Reopen();
                _wake.Set();
                return;
            }
            _http.Close();
            if (state == ConsentState.Denied)
                Clear();
            else
                _http.Reopen(); // Undecided: nothing is sent (the worker checks consent), nothing deleted
        }

        /// <summary>Deletes every queued upload and its files (consent denied, device id reset). Any thread.</summary>
        public void Clear()
        {
            _http.Close();
            _clearRequested = true;
            _wake.Set();
        }

        /// <summary>Main thread: stops the worker (the state stays on disk for the next launch).</summary>
        public void Shutdown(int waitMs)
        {
            _stop = true;
            _http.Close();
            _wake.Set();
            try
            {
                _thread?.Join(Math.Max(0, waitMs));
            }
            catch
            {
                // ignored: a background thread
            }
        }

        // ------------------------------------------------------------------
        // Worker

        private void Run()
        {
            try
            {
                _buffer = new byte[SessionArchive.BufferSize];
                Directory.CreateDirectory(_root);
                LoadAll();
                while (!_stop)
                {
                    if (_clearRequested)
                    {
                        ClearAll();
                        continue;
                    }
                    TakeIncoming();
                    PublishSnapshot();
                    if (_records.Count == 0)
                    {
                        _hasWork = false;
                        _wake.WaitOne(60000);
                        continue;
                    }
                    _hasWork = true;
                    if (_paused || !_allowed())
                    {
                        _wake.WaitOne(5000);
                        continue;
                    }
                    var r = NextReady(out long waitMs);
                    if (r == null)
                    {
                        _wake.WaitOne((int)Math.Min(Math.Max(waitMs, 50), 60000));
                        continue;
                    }
                    try
                    {
                        Step(r);
                    }
                    catch (OperationCanceledException)
                    {
                        // Clear or shutdown during a step.
                    }
                    catch (Exception ex)
                    {
                        _lastError = ex.Message;
                        ApmClient.Log(LogLevel.Warning, $"Session files: {r.Recording}: {ex.Message}");
                        Backoff(r, 0);
                    }
                    AfterStep?.Invoke();
                }
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("SessionFiles", ex);
            }
        }

        private bool Cancelled() => _stop || _clearRequested;

        private string StatePath(string id) => Path.Combine(_root, id + ".state");
        private string ZipPath(string id) => Path.Combine(_root, id + ".zip");
        private string PartPath(string id) => Path.Combine(_root, id + ".part");
        private string SessionJsonPath(string id) => Path.Combine(_root, id + ".session.json");

        private void LoadAll()
        {
            var ids = new HashSet<string>();
            foreach (var path in Directory.GetFiles(_root))
            {
                string file = Path.GetFileName(path);
                int dot = file.IndexOf('.');
                string id = dot > 0 ? file.Substring(0, dot) : file;
                if (file.EndsWith(".state", StringComparison.Ordinal) || file.EndsWith(".state.tmp", StringComparison.Ordinal))
                    ids.Add(id);
            }
            foreach (var id in ids)
            {
                var r = SessionFileRecord.Load(StatePath(id)) ?? SessionFileRecord.Load(StatePath(id) + ".tmp");
                if (r == null || r.Id != id || r.State == SessionUploadState.Done || r.State == SessionUploadState.Failed)
                {
                    DeleteFiles(id);
                    continue;
                }
                _records.Add(r);
                Interlocked.Increment(ref _pendingCount);
            }
            // Files without a state (a crash between steps): not ours to keep.
            foreach (var path in Directory.GetFiles(_root))
            {
                string file = Path.GetFileName(path);
                int dot = file.IndexOf('.');
                string id = dot > 0 ? file.Substring(0, dot) : file;
                if (!ids.Contains(id))
                    TryDelete(path);
            }
            _records.Sort((a, b) => a.CreatedAtMs.CompareTo(b.CreatedAtMs));
        }

        private void TakeIncoming()
        {
            List<SessionFileRecord> taken = null;
            lock (_lock)
            {
                if (_incoming.Count > 0)
                {
                    taken = new List<SessionFileRecord>(_incoming);
                    _incoming.Clear();
                }
            }
            if (taken == null)
                return;
            foreach (var r in taken)
            {
                r.Save(StatePath(r.Id));
                _records.Add(r);
            }
        }

        private void ClearAll()
        {
            _clearRequested = false;
            lock (_lock)
            {
                Interlocked.Add(ref _pendingCount, -(_incoming.Count + _records.Count));
                _incoming.Clear();
            }
            _records.Clear();
            _notBefore.Clear();
            _synced.Clear();
            try
            {
                if (Directory.Exists(_root))
                {
                    foreach (var path in Directory.GetFiles(_root))
                        TryDelete(path);
                }
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Session files: clearing failed: {ex.Message}");
            }
            lock (_finished)
                _finished.Clear();
            PublishSnapshot();
            _http.Reopen();
        }

        private void PublishSnapshot()
        {
            var list = new List<SessionUploadInfo>();
            lock (_finished)
                list.AddRange(_finished);
            foreach (var r in _records)
                list.Add(Info(r));
            lock (_lock)
            {
                foreach (var r in _incoming)
                    list.Add(Info(r));
            }
            _snapshot = list.ToArray();
        }

        private static SessionUploadInfo Info(SessionFileRecord r) => new SessionUploadInfo
        {
            UploadId = r.Id, Directory = r.Directory, Recording = r.Recording, State = r.State, SizeBytes = r.Size,
            PartsSent = r.AckedCount, PartCount = r.PartCount, Error = r.Error
        };

        private SessionFileRecord NextReady(out long waitMs)
        {
            long now = NowMs();
            waitMs = 60000;
            foreach (var r in _records)
            {
                if (_notBefore.TryGetValue(r.Id, out long at) && at > now)
                {
                    waitMs = Math.Min(waitMs, at - now);
                    continue;
                }
                return r;
            }
            return null;
        }

        private void Step(SessionFileRecord r)
        {
            long ageMs = NowMs() - r.CreatedAtMs;
            if (ageMs > (long)Math.Max(1, _config.SessionFileMaxAgeDays) * 86400000L)
            {
                Finish(r, SessionUploadState.Failed, $"not uploaded within {_config.SessionFileMaxAgeDays} days");
                return;
            }
            switch (r.State)
            {
                case SessionUploadState.Queued:
                    Pack(r);
                    break;
                case SessionUploadState.Packed:
                    if (NetworkReady(r))
                        CreateOnServer(r);
                    break;
                case SessionUploadState.Uploading:
                    if (!NetworkReady(r))
                        break;
                    if (!_synced.Contains(r.Id))
                    {
                        CreateOnServer(r); // resume: the server says which parts it has
                        break;
                    }
                    int part = r.NextMissingPart();
                    if (part < 0)
                        Commit(r);
                    else
                        SendPart(r, part);
                    break;
            }
            PublishSnapshot();
        }

        private bool NetworkReady(SessionFileRecord r)
        {
            var reach = (NetworkReachability)_reachability;
            if (reach == NetworkReachability.NotReachable)
            {
                _notBefore[r.Id] = NowMs() + 5000;
                return false;
            }
            if (_config.SessionFilesWifiOnly && reach != NetworkReachability.ReachableViaLocalAreaNetwork)
            {
                _notBefore[r.Id] = NowMs() + 30000;
                return false;
            }
            return true;
        }

        // ---------------- pack

        private void Pack(SessionFileRecord r)
        {
            if (!Directory.Exists(r.Directory))
            {
                Finish(r, SessionUploadState.Failed, "the directory does not exist (any more)");
                return;
            }
            string sessionJson = Path.Combine(r.Directory, "session.json");
            if (!File.Exists(sessionJson))
            {
                Finish(r, SessionUploadState.Failed, "not an AiTelemetry session directory: no session.json");
                return;
            }
            var before = SessionArchive.Snapshot(r.Directory);
            long raw = 0;
            foreach (var f in before)
                raw += f.Length;
            if (raw > _config.MaxSessionFileStorageBytes)
            {
                Finish(r, SessionUploadState.Failed, $"{raw} bytes is more than MaxSessionFileStorageBytes ({_config.MaxSessionFileStorageBytes})");
                return;
            }
            long used = StoredBytes();
            if (used + raw > _config.MaxSessionFileStorageBytes)
            {
                // Wait for earlier uploads to free space (their zips are deleted when done).
                _notBefore[r.Id] = NowMs() + 60000;
                ApmClient.Log(LogLevel.Info, $"Session files: {r.Recording} waits for local space ({used} bytes used)");
                return;
            }

            // A copy of session.json: the create request needs it, also after
            // AiTelemetry's retention deleted the directory.
            CopyCapped(sessionJson, SessionJsonPath(r.Id), MaxSessionJsonBytes);
            if (string.IsNullOrEmpty(r.SessionId))
            {
                // The session the recording was made in, when OZX wrote it into
                // session.json; else the session Upload was called in.
                string apmSession = JsonField(File.ReadAllText(SessionJsonPath(r.Id)), "apmSessionId");
                r.SessionId = IsValidId(apmSession) ? apmSession : r.CurrentSession;
            }
            if (!IsValidId(r.SessionId))
                r.SessionId = IsValidId(r.CurrentSession) ? r.CurrentSession : null; // the server accepts none rather than a bad one

            string tmp = ZipPath(r.Id) + ".tmp";
            var result = SessionArchive.Create(before, tmp, _buffer, Cancelled);
            var after = SessionArchive.Snapshot(r.Directory);
            if (!SessionArchive.SameSnapshot(before, after))
            {
                TryDelete(tmp);
                r.PackAttempts++;
                if (r.PackAttempts >= MaxPackAttempts)
                {
                    Finish(r, SessionUploadState.Failed, "the directory kept changing while it was zipped: upload it after AiTelemetry ended the session");
                    return;
                }
                r.Save(StatePath(r.Id));
                _notBefore[r.Id] = NowMs() + 10000;
                return;
            }
            if (File.Exists(ZipPath(r.Id)))
                File.Delete(ZipPath(r.Id));
            File.Move(tmp, ZipPath(r.Id));
            r.Size = result.Size;
            r.Sha256 = result.Sha256;
            r.FileCount = result.FileCount;
            r.RawBytes = result.RawBytes;
            r.PartBytes = ClampPart(r.PartBytes);
            r.PartCount = (int)((r.Size + r.PartBytes - 1) / r.PartBytes);
            r.Acked = new string('0', r.PartCount);
            r.State = SessionUploadState.Packed;
            r.Save(StatePath(r.Id));
            ApmClient.Log(LogLevel.Info, $"Session files: {r.Recording} zipped: {r.FileCount} files, {r.RawBytes} -> {r.Size} bytes ({(result.Deflated ? "deflate" : "stored")})");
        }

        private long StoredBytes()
        {
            long total = 0;
            try
            {
                foreach (var path in Directory.GetFiles(_root, "*.zip"))
                    total += new FileInfo(path).Length;
            }
            catch (IOException)
            {
            }
            return total;
        }

        private void CopyCapped(string from, string to, int max)
        {
            using (var input = new FileStream(from, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete))
            using (var output = new FileStream(to, FileMode.Create, FileAccess.Write))
            {
                int total = 0;
                int n;
                while (total < max && (n = input.Read(_buffer, 0, Math.Min(_buffer.Length, max - total))) > 0)
                {
                    output.Write(_buffer, 0, n);
                    total += n;
                }
            }
        }

        // ---------------- protocol

        private SessionHttpJob Job(string method, string url, int timeoutSeconds)
        {
            return new SessionHttpJob
            {
                Method = method,
                Url = url,
                TimeoutSeconds = timeoutSeconds,
                Headers = new[]
                {
                    "X-Apm-Upload-Key", _config.SessionFilesUploadKey ?? "",
                    "X-App-Key", _config.AppKey ?? "",
                    ApmClient.SdkVersionHeader, ApmClient.SdkVersion
                }
            };
        }

        /// <summary>Sends and waits; false (and backed off) when no answer came.</summary>
        private bool Exchange(SessionFileRecord r, SessionHttpJob job)
        {
            int waitMs = (job.TimeoutSeconds + 30) * 1000;
            bool ok = _http.Send(job, waitMs);
            job.Done.Close();
            if (Cancelled())
                throw new OperationCanceledException();
            if (!ok || job.TransportError || job.Status == 0)
            {
                _lastError = job.Error ?? "no response";
                Backoff(r, 0);
                return false;
            }
            return true;
        }

        private void CreateOnServer(SessionFileRecord r)
        {
            string sessionJson = "";
            try
            {
                if (File.Exists(SessionJsonPath(r.Id)))
                    sessionJson = File.ReadAllText(SessionJsonPath(r.Id), Encoding.UTF8);
            }
            catch (IOException)
            {
            }
            var sb = new StringBuilder(512 + sessionJson.Length);
            sb.Append('{');
            JsonPair(sb, "upload_id", r.Id);
            JsonPair(sb, "device_id", r.DeviceId);
            JsonPair(sb, "session_id", r.SessionId);
            JsonPair(sb, "recording_name", r.Recording);
            JsonPair(sb, "app_version", r.AppVersion);
            JsonPair(sb, "build", r.Build);
            JsonPair(sb, "build_type", r.BuildType);
            JsonPair(sb, "platform", r.Platform);
            JsonPair(sb, "device_model", r.DeviceModel);
            JsonPair(sb, "os_version", r.OsVersion);
            JsonPair(sb, "unity_version", r.UnityVersion);
            JsonPair(sb, "sdk_version", ApmClient.SdkVersion);
            JsonPair(sb, "git_commit", r.GitCommit);
            JsonPair(sb, "sha256", r.Sha256);
            JsonPair(sb, "session_json", sessionJson);
            sb.Append("\"size_bytes\":").Append(r.Size.ToString(CultureInfo.InvariantCulture));
            sb.Append(",\"part_bytes\":").Append(r.PartBytes.ToString(CultureInfo.InvariantCulture));
            sb.Append(",\"part_count\":").Append(r.PartCount.ToString(CultureInfo.InvariantCulture));
            sb.Append(",\"file_count\":").Append(r.FileCount.ToString(CultureInfo.InvariantCulture));
            sb.Append(",\"raw_bytes\":").Append(r.RawBytes.ToString(CultureInfo.InvariantCulture));
            sb.Append('}');
            var job = Job("POST", _url, Math.Max(5, (int)_config.RequestTimeoutSeconds));
            job.Body = Encoding.UTF8.GetBytes(sb.ToString());
            job.ContentType = "application/json";
            if (!Exchange(r, job))
                return;
            if (job.Status != 200)
            {
                Refused(r, job, "create");
                return;
            }
            _failures = 0;
            if (JsonField(job.ResponseText, "status") == "complete")
            {
                Finish(r, SessionUploadState.Done, null);
                return;
            }
            var received = JsonIntArray(job.ResponseText, "received");
            var acked = new char[r.PartCount];
            for (int i = 0; i < acked.Length; i++)
                acked[i] = '0';
            if (received != null)
            {
                foreach (int n in received)
                {
                    if (n >= 0 && n < acked.Length)
                        acked[n] = '1';
                }
            }
            r.Acked = new string(acked);
            r.State = SessionUploadState.Uploading;
            r.Save(StatePath(r.Id));
            _synced.Add(r.Id);
        }

        private void SendPart(SessionFileRecord r, int n)
        {
            long offset = (long)n * r.PartBytes;
            int length = (int)Math.Min(r.PartBytes, r.Size - offset);
            string partPath = PartPath(r.Id);
            string sha;
            using (var hash = SHA256.Create())
            using (var zip = new FileStream(ZipPath(r.Id), FileMode.Open, FileAccess.Read, FileShare.Read))
            using (var part = new FileStream(partPath, FileMode.Create, FileAccess.Write))
            {
                if (zip.Length != r.Size)
                {
                    Finish(r, SessionUploadState.Failed, "the local archive changed size");
                    return;
                }
                zip.Seek(offset, SeekOrigin.Begin);
                int left = length;
                while (left > 0)
                {
                    int got = zip.Read(_buffer, 0, Math.Min(_buffer.Length, left));
                    if (got <= 0)
                        throw new IOException("the local archive is shorter than recorded");
                    hash.TransformBlock(_buffer, 0, got, null, 0);
                    part.Write(_buffer, 0, got);
                    left -= got;
                }
                hash.TransformFinalBlock(_buffer, 0, 0);
                sha = SessionArchive.Hex(hash.Hash);
            }
            var job = Job("PUT", _url + "/" + r.Id + "/parts/" + n.ToString(CultureInfo.InvariantCulture),
                Math.Max(30, (int)_config.RequestTimeoutSeconds * 4));
            job.BodyFile = partPath;
            job.ContentType = "application/octet-stream";
            var headers = new List<string>(job.Headers) { "X-Apm-Device-Id", r.DeviceId ?? "", "X-Apm-Part-Sha256", sha };
            job.Headers = headers.ToArray();
            long start = NowMs();
            if (!Exchange(r, job))
                return;
            if (job.Status != 200)
            {
                Refused(r, job, "part " + n);
                return;
            }
            _failures = 0;
            Interlocked.Add(ref _bytesSent, length);
            r.SetAcked(n);
            r.Save(StatePath(r.Id));
            // Bandwidth: on average at most SessionFilesBytesPerSecond.
            int bps = _config.SessionFilesBytesPerSecond;
            if (bps > 0)
            {
                long wantMs = (long)length * 1000 / bps;
                long spent = NowMs() - start;
                if (wantMs > spent)
                    Sleep((int)Math.Min(wantMs - spent, 60000));
            }
        }

        private void Sleep(int ms)
        {
            if (Pace != null)
            {
                Pace(ms);
                return;
            }
            _wake.WaitOne(ms); // a Clear / Shutdown wakes it
        }

        private void Commit(SessionFileRecord r)
        {
            var sb = new StringBuilder(160);
            sb.Append('{');
            JsonPair(sb, "device_id", r.DeviceId);
            JsonPair(sb, "sha256", r.Sha256);
            sb.Append("\"size_bytes\":").Append(r.Size.ToString(CultureInfo.InvariantCulture)).Append('}');
            var job = Job("POST", _url + "/" + r.Id + "/commit", 300);
            job.Body = Encoding.UTF8.GetBytes(sb.ToString());
            job.ContentType = "application/json";
            if (!Exchange(r, job))
                return;
            if (job.Status == 200)
            {
                _failures = 0;
                Finish(r, SessionUploadState.Done, null);
                return;
            }
            if (job.Status == 409 && JsonField(job.ResponseText, "reject_code") == null)
            {
                // Parts the server does not have (e.g. its parts were cleaned up): send them again.
                var missing = JsonIntArray(job.ResponseText, "missing");
                if (missing != null && missing.Count > 0)
                {
                    var acked = r.Acked.ToCharArray();
                    foreach (int n in missing)
                    {
                        if (n >= 0 && n < acked.Length)
                            acked[n] = '0';
                    }
                    r.Acked = new string(acked);
                    r.Save(StatePath(r.Id));
                    return;
                }
            }
            Refused(r, job, "commit");
        }

        /// <summary>A non-200 answer: dropped on a reject_code, created again on 404, retried otherwise.</summary>
        private void Refused(SessionFileRecord r, SessionHttpJob job, string what)
        {
            string code = JsonField(job.ResponseText, "reject_code");
            string message = JsonField(job.ResponseText, "error") ?? job.Error ?? ("HTTP " + job.Status);
            _lastError = $"{what}: HTTP {job.Status} {message}";
            if (job.Status >= 400 && job.Status < 500 && job.Status != 408 && job.Status != 429 && !string.IsNullOrEmpty(code))
            {
                Finish(r, SessionUploadState.Failed, $"refused by the server ({code}): {message}");
                return;
            }
            if (job.Status == 404)
            {
                // The server does not know the upload (never created, or cleaned
                // up after days without progress): create it again.
                r.State = SessionUploadState.Packed;
                _synced.Remove(r.Id);
                r.Save(StatePath(r.Id));
                return;
            }
            ApmClient.Log(LogLevel.Info, $"Session files: {r.Recording}: {_lastError}; retried later");
            Backoff(r, ParseRetryAfter(job.RetryAfter));
        }

        private void Backoff(SessionFileRecord r, float retryAfterSeconds)
        {
            _failures++;
            // Full jitter between 1 s and 5 s * 2^(n-1), at most an hour (as EventReporter;
            // not called: its type initializer creates profiler markers).
            double ceiling = Math.Min(3600.0, 5.0 * Math.Pow(2, Math.Min(_failures - 1, 20)));
            double delay = Math.Max(1.0, ceiling * _random.NextDouble());
            delay = Math.Max(delay, retryAfterSeconds);
            _notBefore[r.Id] = NowMs() + (long)(delay * 1000);
        }

        /// <summary>Seconds from a Retry-After header (delta-seconds), at most an hour.</summary>
        internal static float ParseRetryAfter(string header)
        {
            if (string.IsNullOrEmpty(header))
                return 0f;
            if (float.TryParse(header.Trim(), NumberStyles.Float, CultureInfo.InvariantCulture, out float seconds) && seconds > 0f)
                return Math.Min(seconds, 3600f);
            return 0f;
        }

        private void Finish(SessionFileRecord r, SessionUploadState state, string error)
        {
            r.State = state;
            r.Error = error;
            if (_records.Remove(r))
                Interlocked.Decrement(ref _pendingCount);
            _notBefore.Remove(r.Id);
            _synced.Remove(r.Id);
            DeleteFiles(r.Id);
            if (state == SessionUploadState.Done)
            {
                Interlocked.Increment(ref _completed);
                ApmClient.Log(LogLevel.Info, $"Session files: {r.Recording} uploaded ({r.Size} bytes, upload {r.Id})");
            }
            else
            {
                Interlocked.Increment(ref _failed);
                _lastError = error;
                ApmClient.Log(LogLevel.Warning, $"Session files: {r.Recording} dropped: {error}");
            }
            lock (_finished)
            {
                _finished.Add(Info(r));
                if (_finished.Count > MaxFinished)
                    _finished.RemoveAt(0);
            }
            PublishSnapshot();
        }

        private void DeleteFiles(string id)
        {
            TryDelete(ZipPath(id));
            TryDelete(ZipPath(id) + ".tmp");
            TryDelete(PartPath(id));
            TryDelete(SessionJsonPath(id));
            TryDelete(StatePath(id));
            TryDelete(StatePath(id) + ".tmp");
        }

        private static void TryDelete(string path)
        {
            try
            {
                if (File.Exists(path))
                    File.Delete(path);
            }
            catch (Exception)
            {
                // Left for the next launch's scan.
            }
        }

        // ---------------- tiny JSON helpers (the server's small answers)

        private static void JsonPair(StringBuilder sb, string key, string value)
        {
            if (value == null)
                return;
            sb.Append('"').Append(key).Append("\":\"");
            foreach (char c in value)
            {
                switch (c)
                {
                    case '"': sb.Append("\\\""); break;
                    case '\\': sb.Append("\\\\"); break;
                    case '\n': sb.Append("\\n"); break;
                    case '\r': sb.Append("\\r"); break;
                    case '\t': sb.Append("\\t"); break;
                    default:
                        if (c < 0x20 || c == '\u2028' || c == '\u2029')
                            sb.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                        else
                            sb.Append(c);
                        break;
                }
            }
            sb.Append("\",");
        }

        private static int ValueStart(string text, string key)
        {
            if (string.IsNullOrEmpty(text))
                return -1;
            string quoted = "\"" + key + "\"";
            int i = 0;
            while ((i = text.IndexOf(quoted, i, StringComparison.Ordinal)) >= 0)
            {
                int j = i + quoted.Length;
                while (j < text.Length && char.IsWhiteSpace(text[j]))
                    j++;
                if (j < text.Length && text[j] == ':')
                {
                    j++;
                    while (j < text.Length && char.IsWhiteSpace(text[j]))
                        j++;
                    return j;
                }
                i = j;
            }
            return -1;
        }

        /// <summary>The string value of "key" (simple escapes), or null.</summary>
        internal static string JsonField(string text, string key)
        {
            int i = ValueStart(text, key);
            if (i < 0 || i >= text.Length || text[i] != '"')
                return null;
            var sb = new StringBuilder();
            for (i++; i < text.Length; i++)
            {
                char c = text[i];
                if (c == '"')
                    return sb.ToString();
                if (c == '\\' && i + 1 < text.Length)
                {
                    char n = text[++i];
                    switch (n)
                    {
                        case 'n': sb.Append('\n'); break;
                        case 't': sb.Append('\t'); break;
                        case 'r': sb.Append('\r'); break;
                        case 'u':
                            if (i + 4 < text.Length && int.TryParse(text.Substring(i + 1, 4), NumberStyles.HexNumber, CultureInfo.InvariantCulture, out int cp))
                            {
                                sb.Append((char)cp);
                                i += 4;
                            }
                            break;
                        default: sb.Append(n); break;
                    }
                    continue;
                }
                sb.Append(c);
            }
            return null;
        }

        /// <summary>The integers of "key":[...], or null.</summary>
        internal static List<int> JsonIntArray(string text, string key)
        {
            int i = ValueStart(text, key);
            if (i < 0 || i >= text.Length || text[i] != '[')
                return null;
            var list = new List<int>();
            int value = 0;
            bool inNumber = false;
            for (i++; i < text.Length; i++)
            {
                char c = text[i];
                if (c >= '0' && c <= '9')
                {
                    value = value * 10 + (c - '0');
                    inNumber = true;
                    continue;
                }
                if (inNumber)
                {
                    list.Add(value);
                    value = 0;
                    inNumber = false;
                }
                if (c == ']')
                    return list;
            }
            return null;
        }
    }
}
