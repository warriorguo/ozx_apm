using System;
using System.Globalization;
using System.Text;
using Unity.Collections;
using Unity.Profiling;
using UnityEngine;
using UnityEngine.Networking;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Network
{
    /// <summary>
    /// What to do with a stored batch after an upload attempt.
    /// </summary>
    public enum UploadOutcome
    {
        /// <summary>The server stored the batch: delete it locally.</summary>
        Delivered,
        /// <summary>Anything that is not an explicit server verdict: keep the batch and retry later.</summary>
        Retry,
        /// <summary>The server explicitly rejected the payload (reject_code): move it to quarantine.</summary>
        Rejected
    }

    /// <summary>An in-flight upload request.</summary>
    public interface IUploadRequest : IDisposable
    {
        bool IsDone { get; }
        long ResponseCode { get; }
        /// <summary>No HTTP response at all (offline, DNS, timeout, connection reset).</summary>
        bool TransportError { get; }
        string Error { get; }
        string ResponseBody { get; }
        string RetryAfter { get; }
        /// <summary>Copies up to dst.Length bytes of the response body into dst (no allocation). Returns the count.</summary>
        int CopyResponse(byte[] dst);
        /// <summary>Starts the request (UnityWebRequest.SendWebRequest).</summary>
        void Start();
        void Abort();
    }

    /// <summary>Starts upload requests. The default implementation uses UnityWebRequest.</summary>
    public interface IUploadTransport
    {
        /// <summary>
        /// Builds (does not start) a request for the first <paramref name="length"/> bytes
        /// of <paramref name="payload"/>; the buffer may be reused afterwards. Start() sends it.
        /// </summary>
        IUploadRequest Create(string url, byte[] payload, int length, bool compressed, string appKey, int timeoutSeconds);
    }

    /// <summary>
    /// Uploads stored batches to the server, one request at a time, oldest first.
    /// Driven from ApmClient.Update; never blocks the main thread.
    ///
    /// - Uploads start InitialUploadDelaySeconds after Initialize, or earlier
    ///   when the game marks TTI or calls Flush, so they stay out of the way of
    ///   the game's own loading.
    /// - A backlog is sent in merged requests of up to MaxBatchBytes, at most one
    ///   per UploadIntervalSeconds and on average UploadBytesPerSecond.
    /// - Failures are retried with exponential backoff with full jitter
    ///   (RetryBaseDelaySeconds doubling up to RetryMaxDelaySeconds), honouring
    ///   Retry-After, and immediately when the network comes back or the app
    ///   resumes. Nothing is discarded for failing.
    /// - A batch leaves the queue only on an explicit server verdict: 2xx
    ///   (delivered) or a 4xx whose JSON body carries "reject_code"
    ///   (quarantined). A bare 4xx from a proxy or middleware is retried.
    /// </summary>
    public class EventReporter
    {
        // Each request's main-thread work is spread over separate frames
        // (create, start, read result, dispose), so no single frame carries all of it.
        private enum State
        {
            Idle,
            WaitingForBatch,
            Created,
            Sending,
            Disposing
        }

        // How often to look for newly sealed batches when there is nothing to send.
        private const float IdlePollSeconds = 1f;
        private const float ReachabilityPollSeconds = 1f;
        private const float MaxRetryAfterSeconds = 3600f;

        private static readonly ProfilerMarker s_sendMarker = new ProfilerMarker("OzxApm.Upload.Send");
        private static readonly ProfilerMarker s_completeMarker = new ProfilerMarker("OzxApm.Upload.Complete");
        private static readonly ProfilerMarker s_pollMarker = new ProfilerMarker("OzxApm.Upload.Poll");
        private static readonly ProfilerMarker s_readMarker = new ProfilerMarker("OzxApm.Upload.Complete.Read");
        private static readonly ProfilerMarker s_disposeMarker = new ProfilerMarker("OzxApm.Upload.Dispose");
        private static readonly ProfilerMarker s_startMarker = new ProfilerMarker("OzxApm.Upload.Start");

        private static readonly byte[] RetryKey = Encoding.ASCII.GetBytes("\"retry\"");
        private static readonly byte[] RejectCodeKey = Encoding.ASCII.GetBytes("\"reject_code\"");
        private static readonly byte[] TrueLiteral = Encoding.ASCII.GetBytes("true");

        private readonly ApmConfig _config;
        private readonly OfflineStorage _storage;
        private readonly NetworkLogger _networkLogger;
        private readonly IUploadTransport _transport;
        private readonly Func<float> _clock;
        private readonly Func<NetworkReachability> _reachability;
        private readonly string _ingestUrl;
        private readonly System.Random _random; // not UnityEngine.Random: the game owns that state

        private State _state = State.Idle;
        private float _nextAttemptTime;
        // After a delivered request: earliest time for exceptions/crashes/startup,
        // which do not wait out the size-based pause of a large (e.g. held-back
        // perf) request. Failures set it to the backoff, so it never shortens a retry.
        private float _highValueAttemptTime;
        private float _uploadsAllowedAt;
        private int _consecutiveFailures;

        private IUploadRequest _request;
        private IUploadRequest _finished; // completed request, disposed on the next frame
        // Response bodies are small JSON objects; the verdict fields come first.
        private readonly byte[] _responseBuffer = new byte[1024];
        private UploadBatch _batch;
        private float _requestStartTime;

        private float _nextReachabilityCheck;
        private NetworkReachability _lastReachability = NetworkReachability.ReachableViaLocalAreaNetwork;

        public int ConsecutiveFailures => _consecutiveFailures;
        /// <summary>Main-thread cost of the last request start / completion (Stopwatch ticks).</summary>
        public long LastSendTicks { get; private set; }
        public long LastStartTicks { get; private set; }
        public long LastCompleteTicks { get; private set; }
        public long LastDisposeTicks { get; private set; }
        public bool IsSending => _state == State.Sending;
        /// <summary>Time (on the reporter's clock) of the next upload attempt.</summary>
        public float NextAttemptTime => Mathf.Max(_nextAttemptTime, _uploadsAllowedAt);

        public EventReporter(ApmConfig config, OfflineStorage storage, NetworkLogger networkLogger)
            : this(config, storage, networkLogger, new UnityWebRequestTransport(),
                   () => Time.realtimeSinceStartup, () => Application.internetReachability, new System.Random())
        {
        }

        internal EventReporter(ApmConfig config, OfflineStorage storage, NetworkLogger networkLogger,
            IUploadTransport transport, Func<float> clock, Func<NetworkReachability> reachability, System.Random random)
        {
            _config = config;
            _storage = storage;
            _networkLogger = networkLogger;
            _transport = transport;
            _clock = clock;
            _reachability = reachability;
            _random = random;
            _ingestUrl = config.ServerUrl.TrimEnd('/') + "/v1/events";

            float now = _clock();
            _uploadsAllowedAt = now + Mathf.Max(0f, config.InitialUploadDelaySeconds);
            _nextAttemptTime = now;
            _nextReachabilityCheck = now;
        }

        /// <summary>
        /// Advances the upload state machine. Call once per frame on the main thread.
        /// Allocation-free unless a request is created or completed.
        /// </summary>
        public void Update()
        {
            float now = _clock();

            if (now >= _nextReachabilityCheck)
            {
                _nextReachabilityCheck = now + ReachabilityPollSeconds;
                var reachability = _reachability();
                if (_lastReachability == NetworkReachability.NotReachable &&
                    reachability != NetworkReachability.NotReachable)
                {
                    // Network is back: retry right away instead of waiting out the backoff.
                    RetryNow();
                }
                _lastReachability = reachability;
            }

            switch (_state)
            {
                case State.Idle:
                    if ((now >= _nextAttemptTime || (now >= _highValueAttemptTime && _storage.HighValueWaiting)) &&
                        now >= _uploadsAllowedAt && _config.Consent == ConsentState.Granted)
                    {
                        _storage.RequestUpload();
                        _state = State.WaitingForBatch;
                    }
                    break;

                case State.WaitingForBatch:
                    UploadPoll poll;
                    UploadBatch batch;
                    using (s_pollMarker.Auto())
                        poll = _storage.PollUpload(out batch);
                    if (poll == UploadPoll.Ready)
                    {
                        if (_config.Consent != ConsentState.Granted)
                        {
                            // Consent was withdrawn meanwhile: do not send.
                            _storage.CompleteUpload(batch, UploadOutcome.Retry);
                            _state = State.Idle;
                        }
                        else
                        {
                            long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
                            using (s_sendMarker.Auto())
                                Send(batch, now);
                            LastSendTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
                        }
                    }
                    else if (poll == UploadPoll.Empty)
                    {
                        _state = State.Idle;
                        _nextAttemptTime = Mathf.Max(_nextAttemptTime, now + IdlePollSeconds);
                    }
                    break;

                case State.Created:
                    {
                        long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
                        using (s_startMarker.Auto())
                            StartRequest(now);
                        LastStartTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
                    }
                    break;

                case State.Sending:
                    if (_request != null && _request.IsDone)
                    {
                        long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
                        using (s_completeMarker.Auto())
                            OnRequestDone(now);
                        LastCompleteTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
                    }
                    break;

                case State.Disposing:
                    {
                        long t0 = System.Diagnostics.Stopwatch.GetTimestamp();
                        using (s_disposeMarker.Auto())
                            DisposeFinished();
                        LastDisposeTicks = System.Diagnostics.Stopwatch.GetTimestamp() - t0;
                        _state = State.Idle;
                    }
                    break;
            }
        }

        /// <summary>
        /// Retry as soon as possible (network came back, app resumed). Keeps the
        /// initial delay for uploads in place.
        /// </summary>
        public void RetryNow()
        {
            _nextAttemptTime = _clock();
        }

        /// <summary>
        /// The game finished loading (TTI) or asked for a flush: uploads may start now.
        /// </summary>
        public void NotifyGameReady()
        {
            float now = _clock();
            if (_uploadsAllowedAt > now)
                _uploadsAllowedAt = now;
        }

        /// <summary>
        /// Aborts an in-flight request. Its batch stays on disk and is sent again later.
        /// </summary>
        public void Shutdown()
        {
            DisposeFinished();
            if (_request != null)
            {
                try
                {
                    _request.Abort();
                    _request.Dispose();
                }
                catch
                {
                    // Shutting down; nothing to do.
                }
                _request = null;
            }
            if (_batch != null)
            {
                _storage.CompleteUpload(_batch, UploadOutcome.Retry);
                _batch = null;
            }
            _state = State.Idle;
        }

        private void Send(UploadBatch batch, float now)
        {
            _batch = batch;
            _requestStartTime = now;

            try
            {
                if (_networkLogger != null && _networkLogger.IsEnabled)
                {
                    if (batch.Compressed)
                        _networkLogger.LogCompression(batch.UncompressedBytes, batch.PayloadLength);
                    _networkLogger.LogRequest(_ingestUrl, "POST", null, batch.DebugJson, batch.PayloadLength, batch.Compressed);
                }

                _request = _transport.Create(_ingestUrl, batch.PayloadBuffer, batch.PayloadLength, batch.Compressed,
                    _config.AppKey, Mathf.Max(1, (int)_config.RequestTimeoutSeconds));
                _state = State.Created; // started on the next frame
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to start upload: {ex.Message}");
                _request = null;
                Finish(UploadOutcome.Retry, now, 0f);
            }
        }

        private void StartRequest(float now)
        {
            try
            {
                _request.Start();
                _state = State.Sending;
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to start upload: {ex.Message}");
                try { _request.Dispose(); } catch { }
                _request = null;
                Finish(UploadOutcome.Retry, now, 0f);
            }
        }

        private void DisposeFinished()
        {
            if (_finished == null)
                return;
            try { _finished.Dispose(); } catch { }
            _finished = null;
        }

        private void OnRequestDone(float now)
        {
            var request = _request;
            _request = null;

            UploadOutcome outcome;
            long code = 0;
            string body = null;
            string error = null;
            float retryAfter = 0f;
            bool wantText = _networkLogger != null && _networkLogger.IsEnabled;
            try
            {
                using (s_readMarker.Auto())
                {
                    // Classify from the raw response bytes: no strings unless they are logged.
                    code = request.ResponseCode;
                    int length = request.CopyResponse(_responseBuffer);
                    outcome = Classify(code, request.TransportError, _responseBuffer, length);
                    if (code == 429 || code == 503)
                        retryAfter = ParseRetryAfter(request.RetryAfter);
                    if (wantText)
                    {
                        body = request.ResponseBody;
                        error = request.Error;
                    }
                }
            }
            catch (Exception ex)
            {
                error = ex.Message;
                outcome = UploadOutcome.Retry;
            }
            _finished = request; // disposed on the next frame

            double elapsedMs = (now - _requestStartTime) * 1000.0;
            int eventCount = _batch != null ? _batch.EventCount : 0;

            Finish(outcome, now, retryAfter);
            _state = State.Disposing;

            if (outcome == UploadOutcome.Delivered)
            {
                _networkLogger?.LogResponse(_ingestUrl, (int)code, body, elapsedMs, eventCount);
            }
            else
            {
                _networkLogger?.LogFailure(_ingestUrl, (int)code, error, body, elapsedMs, eventCount,
                    _consecutiveFailures, _nextAttemptTime - now);
                if (outcome == UploadOutcome.Rejected)
                {
                    ApmClient.Log(LogLevel.Warning,
                        $"Server rejected a batch of {eventCount} events (HTTP {code}); moved to quarantine");
                }
            }
        }

        private void Finish(UploadOutcome outcome, float now, float retryAfterSeconds)
        {
            int batchBytes = _batch != null ? _batch.UncompressedBytes : 0;
            if (_batch != null)
            {
                _storage.CompleteUpload(_batch, outcome);
                _batch = null;
            }
            _state = State.Idle;

            if (outcome == UploadOutcome.Delivered)
            {
                // Pace the backlog drain by volume: a backlog goes out in large
                // merged requests (up to MaxBatchBytes), spaced so the average rate
                // stays at UploadBytesPerSecond. Each request costs two main-thread
                // frames (start, finish); fewer, larger requests keep those frames
                // rare without slowing the drain.
                _consecutiveFailures = 0;
                _nextAttemptTime = now + ComputeUploadGapSeconds(batchBytes, _config.UploadIntervalSeconds, _config.UploadBytesPerSecond);
                _highValueAttemptTime = now + Mathf.Max(0f, _config.UploadIntervalSeconds);
                return;
            }

            // Retry and Rejected both back off: a server that rejects or fails
            // must not be hit at full speed.
            _consecutiveFailures++;
            float delay = ComputeBackoffSeconds(_consecutiveFailures, _config.RetryBaseDelaySeconds,
                _config.RetryMaxDelaySeconds, (float)_random.NextDouble());
            delay = Mathf.Max(delay, Mathf.Max(_config.UploadIntervalSeconds, retryAfterSeconds));
            _nextAttemptTime = now + delay;
            _highValueAttemptTime = _nextAttemptTime;
        }

        /// <summary>
        /// Time until the next request after a delivered one carrying
        /// <paramref name="bytes"/> uncompressed bytes.
        /// </summary>
        public static float ComputeUploadGapSeconds(int bytes, float minIntervalSeconds, int bytesPerSecond)
        {
            float gap = Mathf.Max(0f, minIntervalSeconds);
            if (bytesPerSecond > 0)
                gap = Mathf.Max(gap, (float)bytes / bytesPerSecond);
            return gap;
        }

        /// <summary>
        /// Exponential backoff with full jitter: a random delay between a small
        /// floor and min(max, base * 2^(failures-1)), so clients that failed
        /// together (e.g. during a server outage) spread out their retries.
        /// </summary>
        public static float ComputeBackoffSeconds(int failures, float baseSeconds, float maxSeconds, float random01)
        {
            if (failures <= 0)
                return 0f;
            baseSeconds = Mathf.Max(0.1f, baseSeconds);
            maxSeconds = Mathf.Max(baseSeconds, maxSeconds);

            int exponent = Mathf.Min(failures - 1, 20);
            float ceiling = Mathf.Min(maxSeconds, baseSeconds * (1 << exponent));
            float floor = Mathf.Min(1f, baseSeconds);
            return Mathf.Max(floor, ceiling * Mathf.Clamp01(random01));
        }

        /// <summary>
        /// Maps an HTTP result to what happens to the stored batch.
        /// Delivered: 2xx unless the body says "retry":true.
        /// Rejected: a 4xx whose JSON body carries a non-empty "reject_code".
        /// Everything else (no connection, timeout, 5xx, 429, bare 4xx from a
        /// proxy or middleware) is retried.
        /// </summary>
        public static UploadOutcome Classify(long httpStatus, bool transportError, string responseBody)
        {
            var bytes = responseBody != null ? Encoding.UTF8.GetBytes(responseBody) : null;
            return Classify(httpStatus, transportError, bytes, bytes != null ? bytes.Length : 0);
        }

        /// <summary>Allocation-free variant on the raw response bytes.</summary>
        public static UploadOutcome Classify(long httpStatus, bool transportError, byte[] body, int length)
        {
            if (transportError || httpStatus == 0)
                return UploadOutcome.Retry;

            if (httpStatus >= 200 && httpStatus < 300)
                return IsTrueField(body, length, RetryKey) ? UploadOutcome.Retry : UploadOutcome.Delivered;

            if (httpStatus >= 400 && httpStatus < 500 && httpStatus != 408 && httpStatus != 429 &&
                IsNonEmptyStringField(body, length, RejectCodeKey))
                return UploadOutcome.Rejected;

            return UploadOutcome.Retry;
        }

        /// <summary>Seconds from a Retry-After header (delta-seconds form only).</summary>
        public static float ParseRetryAfter(string header)
        {
            if (string.IsNullOrEmpty(header))
                return 0f;
            if (float.TryParse(header.Trim(), NumberStyles.Float, CultureInfo.InvariantCulture, out float seconds) && seconds > 0f)
                return Mathf.Min(seconds, MaxRetryAfterSeconds);
            return 0f;
        }

        /// <summary>Index of the first byte of the value of "key": in body, or -1.</summary>
        private static int ValueStart(byte[] body, int length, byte[] key)
        {
            if (body == null)
                return -1;
            for (int i = 0; i <= length - key.Length; i++)
            {
                int k = 0;
                while (k < key.Length && body[i + k] == key[k])
                    k++;
                if (k != key.Length)
                    continue;
                int j = SkipSpace(body, i + key.Length, length);
                if (j >= length || body[j] != (byte)':')
                    continue;
                j = SkipSpace(body, j + 1, length);
                return j < length ? j : -1;
            }
            return -1;
        }

        private static int SkipSpace(byte[] b, int i, int length)
        {
            while (i < length && (b[i] == (byte)' ' || b[i] == (byte)'\t' || b[i] == (byte)'\r' || b[i] == (byte)'\n'))
                i++;
            return i;
        }

        private static bool IsTrueField(byte[] body, int length, byte[] key)
        {
            int i = ValueStart(body, length, key);
            if (i < 0 || i + TrueLiteral.Length > length)
                return false;
            for (int k = 0; k < TrueLiteral.Length; k++)
            {
                if (body[i + k] != TrueLiteral[k])
                    return false;
            }
            return true;
        }

        private static bool IsNonEmptyStringField(byte[] body, int length, byte[] key)
        {
            int i = ValueStart(body, length, key);
            return i >= 0 && i + 1 < length && body[i] == (byte)'"' && body[i + 1] != (byte)'"';
        }
    }

    /// <summary>
    /// UnityWebRequest-based transport. DNS and connect happen off the main thread.
    /// The payload is copied once into a NativeArray whose ownership passes to the
    /// upload handler (no managed copy per request), and the response is read from
    /// the download handler's native buffer.
    /// </summary>
    internal sealed class UnityWebRequestTransport : IUploadTransport
    {
        private static readonly ProfilerMarker s_createMarker = new ProfilerMarker("OzxApm.Upload.Send.Create");
        private static readonly ProfilerMarker s_headersMarker = new ProfilerMarker("OzxApm.Upload.Send.Headers");
        private static readonly ProfilerMarker s_sendMarker = new ProfilerMarker("OzxApm.Upload.Send.SendWebRequest");

        public IUploadRequest Create(string url, byte[] payload, int length, bool compressed, string appKey, int timeoutSeconds)
        {
            UnityWebRequest request;
            using (s_createMarker.Auto())
            {
#if UNITY_2021_2_OR_NEWER
                // UploadHandlerRaw(NativeArray<byte>, bool) exists since 2021.2: one copy
                // into native memory owned by the handler, no managed copy per request.
                var data = new NativeArray<byte>(length, Allocator.Persistent, NativeArrayOptions.UninitializedMemory);
                try
                {
                    NativeArray<byte>.Copy(payload, 0, data, 0, length);
                    request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST)
                    {
                        uploadHandler = new UploadHandlerRaw(data, true), // takes ownership of data
                        downloadHandler = new DownloadHandlerBuffer(),
                        timeout = timeoutSeconds
                    };
                }
                catch
                {
                    if (data.IsCreated)
                        data.Dispose();
                    throw;
                }
#else
                // 2020.3: only UploadHandlerRaw(byte[]) exists, and it sends the whole
                // array, so make an exact-length copy of the pooled buffer.
                var data = new byte[length];
                Buffer.BlockCopy(payload, 0, data, 0, length);
                request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST)
                {
                    uploadHandler = new UploadHandlerRaw(data),
                    downloadHandler = new DownloadHandlerBuffer(),
                    timeout = timeoutSeconds
                };
#endif
            }
            try
            {
                using (s_headersMarker.Auto())
                {
                    request.SetRequestHeader("Content-Type", "application/json");
                    request.SetRequestHeader(ApmClient.SdkVersionHeader, ApmClient.SdkVersion);
                    if (!string.IsNullOrEmpty(appKey))
                        request.SetRequestHeader("X-App-Key", appKey);
                    if (compressed)
                        request.SetRequestHeader("Content-Encoding", "gzip");
                }
                return new Request(request);
            }
            catch
            {
                request.Dispose();
                throw;
            }
        }

        private sealed class Request : IUploadRequest
        {
            private readonly UnityWebRequest _request;

            public Request(UnityWebRequest request)
            {
                _request = request;
            }

            public bool IsDone => _request.isDone;
            public long ResponseCode => _request.responseCode;
            public bool TransportError => _request.result == UnityWebRequest.Result.ConnectionError;
            public string Error => _request.error;
            public string ResponseBody => _request.downloadHandler != null ? _request.downloadHandler.text : null;
            public string RetryAfter => _request.GetResponseHeader("Retry-After");

            public int CopyResponse(byte[] dst)
            {
                var handler = _request.downloadHandler;
                if (handler == null)
                    return 0;
#if UNITY_2021_2_OR_NEWER
                var data = handler.nativeData; // since 2021.2: no managed copy
#else
                var data = handler.data;       // 2020.3: allocates a copy of the body
                if (data == null)
                    return 0;
#endif
                int n = Math.Min(dst.Length, data.Length);
                for (int i = 0; i < n; i++)
                    dst[i] = data[i];
                return n;
            }

            public void Start()
            {
                using (s_sendMarker.Auto())
                    _request.SendWebRequest(); // async: DNS/connect happen off the main thread
            }

            public void Abort() => _request.Abort();
            public void Dispose() => _request.Dispose();
        }
    }
}
