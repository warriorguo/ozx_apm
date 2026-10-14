using System;
using System.Globalization;
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
        void Abort();
    }

    /// <summary>Starts upload requests. The default implementation uses UnityWebRequest.</summary>
    public interface IUploadTransport
    {
        IUploadRequest Send(string url, byte[] payload, bool compressed, string appKey, int timeoutSeconds);
    }

    /// <summary>
    /// Uploads stored batches to the server, one request at a time, oldest first.
    /// Driven from ApmClient.Update; never blocks the main thread.
    ///
    /// - Uploads start InitialUploadDelaySeconds after Initialize, or earlier
    ///   when the game marks TTI or calls Flush, so they stay out of the way of
    ///   the game's own loading.
    /// - The backlog is drained at most one request per UploadIntervalSeconds.
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
        private enum State
        {
            Idle,
            WaitingForBatch,
            Sending
        }

        // How often to look for newly sealed batches when there is nothing to send.
        private const float IdlePollSeconds = 1f;
        private const float ReachabilityPollSeconds = 1f;
        private const float MaxRetryAfterSeconds = 3600f;

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
        private float _uploadsAllowedAt;
        private int _consecutiveFailures;

        private IUploadRequest _request;
        private UploadBatch _batch;
        private float _requestStartTime;

        private float _nextReachabilityCheck;
        private NetworkReachability _lastReachability = NetworkReachability.ReachableViaLocalAreaNetwork;

        public int ConsecutiveFailures => _consecutiveFailures;
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
                    if (now >= _nextAttemptTime && now >= _uploadsAllowedAt && _config.Consent == ConsentState.Granted)
                    {
                        _storage.RequestUpload();
                        _state = State.WaitingForBatch;
                    }
                    break;

                case State.WaitingForBatch:
                    var poll = _storage.PollUpload(out var batch);
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
                            Send(batch, now);
                        }
                    }
                    else if (poll == UploadPoll.Empty)
                    {
                        _state = State.Idle;
                        _nextAttemptTime = Mathf.Max(_nextAttemptTime, now + IdlePollSeconds);
                    }
                    break;

                case State.Sending:
                    if (_request != null && _request.IsDone)
                        OnRequestDone(now);
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
                        _networkLogger.LogCompression(batch.UncompressedBytes, batch.Payload.Length);
                    _networkLogger.LogRequest(_ingestUrl, "POST", null, batch.DebugJson, batch.Payload.Length, batch.Compressed);
                }

                _request = _transport.Send(_ingestUrl, batch.Payload, batch.Compressed, _config.AppKey,
                    Mathf.Max(1, (int)_config.RequestTimeoutSeconds));
                _state = State.Sending;
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to start upload: {ex.Message}");
                _request = null;
                Finish(UploadOutcome.Retry, now, 0f);
            }
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
            try
            {
                code = request.ResponseCode;
                body = request.ResponseBody;
                error = request.Error;
                outcome = Classify(code, request.TransportError, body);
                if (code == 429 || code == 503)
                    retryAfter = ParseRetryAfter(request.RetryAfter);
            }
            catch (Exception ex)
            {
                error = ex.Message;
                outcome = UploadOutcome.Retry;
            }
            finally
            {
                try { request.Dispose(); } catch { }
            }

            double elapsedMs = (now - _requestStartTime) * 1000.0;
            int eventCount = _batch != null ? _batch.EventCount : 0;

            Finish(outcome, now, retryAfter);

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
            if (_batch != null)
            {
                _storage.CompleteUpload(_batch, outcome);
                _batch = null;
            }
            _state = State.Idle;

            if (outcome == UploadOutcome.Delivered)
            {
                // Pace the backlog drain.
                _consecutiveFailures = 0;
                _nextAttemptTime = now + Mathf.Max(0f, _config.UploadIntervalSeconds);
                return;
            }

            // Retry and Rejected both back off: a server that rejects or fails
            // must not be hit at full speed.
            _consecutiveFailures++;
            float delay = ComputeBackoffSeconds(_consecutiveFailures, _config.RetryBaseDelaySeconds,
                _config.RetryMaxDelaySeconds, (float)_random.NextDouble());
            delay = Mathf.Max(delay, Mathf.Max(_config.UploadIntervalSeconds, retryAfterSeconds));
            _nextAttemptTime = now + delay;
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
            if (transportError || httpStatus == 0)
                return UploadOutcome.Retry;

            if (httpStatus >= 200 && httpStatus < 300)
                return BoolField(responseBody, "retry") ? UploadOutcome.Retry : UploadOutcome.Delivered;

            if (httpStatus >= 400 && httpStatus < 500 && httpStatus != 408 && httpStatus != 429 &&
                HasNonEmptyStringField(responseBody, "reject_code"))
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

        private static int ValueStart(string json, string field)
        {
            if (string.IsNullOrEmpty(json))
                return -1;
            int i = json.IndexOf("\"" + field + "\"", StringComparison.Ordinal);
            if (i < 0)
                return -1;
            i += field.Length + 2;
            while (i < json.Length && char.IsWhiteSpace(json[i])) i++;
            if (i >= json.Length || json[i] != ':')
                return -1;
            i++;
            while (i < json.Length && char.IsWhiteSpace(json[i])) i++;
            return i < json.Length ? i : -1;
        }

        private static bool BoolField(string json, string field)
        {
            int i = ValueStart(json, field);
            return i >= 0 && string.CompareOrdinal(json, i, "true", 0, 4) == 0;
        }

        private static bool HasNonEmptyStringField(string json, string field)
        {
            int i = ValueStart(json, field);
            return i >= 0 && i + 1 < json.Length && json[i] == '"' && json[i + 1] != '"';
        }
    }

    /// <summary>
    /// UnityWebRequest-based transport. DNS and connect happen off the main thread.
    /// </summary>
    internal sealed class UnityWebRequestTransport : IUploadTransport
    {
        public IUploadRequest Send(string url, byte[] payload, bool compressed, string appKey, int timeoutSeconds)
        {
            var request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST)
            {
                uploadHandler = new UploadHandlerRaw(payload),
                downloadHandler = new DownloadHandlerBuffer(),
                timeout = timeoutSeconds
            };
            try
            {
                request.SetRequestHeader("Content-Type", "application/json");
                request.SetRequestHeader(ApmClient.SdkVersionHeader, ApmClient.SdkVersion);
                if (!string.IsNullOrEmpty(appKey))
                    request.SetRequestHeader("X-App-Key", appKey);
                if (compressed)
                    request.SetRequestHeader("Content-Encoding", "gzip");
                request.SendWebRequest();
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
            public void Abort() => _request.Abort();
            public void Dispose() => _request.Dispose();
        }
    }
}
