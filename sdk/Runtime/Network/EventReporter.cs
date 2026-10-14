using System;
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
        /// <summary>Transient failure (offline, timeout, 5xx, 429, storage error): keep it and retry later.</summary>
        Retry,
        /// <summary>The server will never accept this payload (malformed / too large): delete it.</summary>
        Rejected
    }

    /// <summary>
    /// Uploads stored batches to the server, one request at a time, oldest first.
    /// Driven from ApmClient.Update; never blocks the main thread. Batches stay
    /// on disk until the server confirms them; failures are retried with
    /// exponential backoff (capped at RetryMaxDelaySeconds) and immediately
    /// when the network comes back. Nothing is discarded for failing.
    /// </summary>
    public class EventReporter
    {
        private enum State
        {
            Idle,
            WaitingForBatch,
            Sending
        }

        // Delay before the first upload, so startup and the first frames are left alone.
        private const float InitialUploadDelaySeconds = 2f;
        // How often to look for newly sealed batches when there is nothing to send.
        private const float IdlePollSeconds = 1f;
        private const float ReachabilityPollSeconds = 1f;

        private readonly ApmConfig _config;
        private readonly OfflineStorage _storage;
        private readonly NetworkLogger _networkLogger;
        private readonly string _ingestUrl;
        private readonly System.Random _random = new System.Random(); // not UnityEngine.Random: the game owns that state

        private State _state = State.Idle;
        private float _nextAttemptTime;
        private int _consecutiveFailures;
        private float _currentBackoffSeconds;

        private UnityWebRequest _request;
        private UploadBatch _batch;
        private float _requestStartTime;

        private float _nextReachabilityCheck;
        private NetworkReachability _lastReachability = NetworkReachability.ReachableViaLocalAreaNetwork;

        public int ConsecutiveFailures => _consecutiveFailures;
        public bool IsSending => _state == State.Sending;

        public EventReporter(ApmConfig config, OfflineStorage storage, NetworkLogger networkLogger)
        {
            _config = config;
            _storage = storage;
            _networkLogger = networkLogger;
            _ingestUrl = config.ServerUrl.TrimEnd('/') + "/v1/events";
            _nextAttemptTime = Time.realtimeSinceStartup + InitialUploadDelaySeconds;
        }

        /// <summary>
        /// Advances the upload state machine. Call once per frame on the main thread.
        /// Allocation-free unless a request is created or completed.
        /// </summary>
        public void Update()
        {
            float now = Time.realtimeSinceStartup;

            if (now >= _nextReachabilityCheck)
            {
                _nextReachabilityCheck = now + ReachabilityPollSeconds;
                var reachability = Application.internetReachability;
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
                    if (now >= _nextAttemptTime && _config.Consent == ConsentState.Granted)
                    {
                        _storage.RequestUpload();
                        _state = State.WaitingForBatch;
                    }
                    break;

                case State.WaitingForBatch:
                    var poll = _storage.PollUpload(out var batch);
                    if (poll == UploadPoll.Ready)
                    {
                        Send(batch, now);
                    }
                    else if (poll == UploadPoll.Empty)
                    {
                        _state = State.Idle;
                        _nextAttemptTime = Mathf.Max(_nextAttemptTime, now + IdlePollSeconds);
                    }
                    break;

                case State.Sending:
                    if (_request != null && _request.isDone)
                        OnRequestDone(now);
                    break;
            }
        }

        /// <summary>
        /// Skips the current backoff (network came back, app resumed, explicit flush).
        /// </summary>
        public void RetryNow()
        {
            _nextAttemptTime = Time.realtimeSinceStartup;
            _currentBackoffSeconds = 0f;
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
                _storage.CompleteUpload(_batch, false);
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
                var request = new UnityWebRequest(_ingestUrl, UnityWebRequest.kHttpVerbPOST)
                {
                    uploadHandler = new UploadHandlerRaw(batch.Payload),
                    downloadHandler = new DownloadHandlerBuffer(),
                    timeout = Mathf.Max(1, (int)_config.RequestTimeoutSeconds)
                };
                request.SetRequestHeader("Content-Type", "application/json");
                if (!string.IsNullOrEmpty(_config.AppKey))
                    request.SetRequestHeader("X-App-Key", _config.AppKey);
                if (batch.Compressed)
                    request.SetRequestHeader("Content-Encoding", "gzip");

                if (_networkLogger != null && _config.LogLevel >= LogLevel.Debug)
                {
                    if (batch.Compressed)
                        _networkLogger.LogCompression(batch.UncompressedBytes, batch.Payload.Length);
                    _networkLogger.LogRequest(_ingestUrl, "POST", null, batch.DebugJson, batch.Payload.Length, batch.Compressed);
                }

                _request = request;
                request.SendWebRequest(); // async: DNS/connect happen off the main thread
                _state = State.Sending;
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to start upload: {ex.Message}");
                if (_request != null)
                {
                    try { _request.Dispose(); } catch { }
                    _request = null;
                }
                Finish(UploadOutcome.Retry, now);
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
            try
            {
                code = request.responseCode;
                bool transportError = request.result == UnityWebRequest.Result.ConnectionError;
                body = request.downloadHandler != null ? request.downloadHandler.text : null;
                error = request.error;
                outcome = Classify(code, transportError, body);
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

            if (outcome == UploadOutcome.Delivered)
            {
                _networkLogger?.LogResponse(_ingestUrl, (int)code, body, elapsedMs, eventCount);
            }
            else
            {
                int failures = outcome == UploadOutcome.Retry ? _consecutiveFailures + 1 : _consecutiveFailures;
                _networkLogger?.LogFailure(_ingestUrl, (int)code, error, body, elapsedMs, eventCount,
                    failures, NextBackoffSeconds(failures));
                if (outcome == UploadOutcome.Rejected)
                {
                    ApmClient.Log(LogLevel.Warning,
                        $"Server rejected a batch of {eventCount} events (HTTP {code}); dropping it");
                }
            }

            Finish(outcome, now);
        }

        private void Finish(UploadOutcome outcome, float now)
        {
            if (_batch != null)
            {
                _storage.CompleteUpload(_batch, outcome != UploadOutcome.Retry);
                _batch = null;
            }
            _state = State.Idle;

            if (outcome == UploadOutcome.Retry)
            {
                _consecutiveFailures++;
                _currentBackoffSeconds = NextBackoffSeconds(_consecutiveFailures);
                _nextAttemptTime = now + _currentBackoffSeconds;
            }
            else
            {
                // Delivered (or permanently rejected): keep draining the backlog.
                _consecutiveFailures = 0;
                _currentBackoffSeconds = 0f;
                _nextAttemptTime = now;
            }
        }

        private float NextBackoffSeconds(int failures)
        {
            return ComputeBackoffSeconds(failures, _config.RetryBaseDelaySeconds, _config.RetryMaxDelaySeconds,
                (float)_random.NextDouble());
        }

        /// <summary>
        /// Exponential backoff: base * 2^(failures-1), capped at max, with +/-20% jitter
        /// so many clients coming back online do not retry in lockstep.
        /// </summary>
        public static float ComputeBackoffSeconds(int failures, float baseSeconds, float maxSeconds, float random01)
        {
            if (failures <= 0)
                return 0f;
            baseSeconds = Mathf.Max(0.1f, baseSeconds);
            maxSeconds = Mathf.Max(baseSeconds, maxSeconds);

            int exponent = Mathf.Min(failures - 1, 20);
            float delay = Mathf.Min(maxSeconds, baseSeconds * (1 << exponent));
            float jitter = 0.8f + 0.4f * Mathf.Clamp01(random01);
            return Mathf.Min(maxSeconds, delay * jitter);
        }

        /// <summary>
        /// Maps an HTTP result to what happens to the stored batch.
        /// Only a 2xx without "stored":false lets the client delete its copy.
        /// 400/413/422 mean the payload itself is bad and would fail forever.
        /// Everything else (no connection, timeout, 401/403/404/408/429, 5xx) is retried.
        /// </summary>
        public static UploadOutcome Classify(long httpStatus, bool transportError, string responseBody)
        {
            if (transportError || httpStatus == 0)
                return UploadOutcome.Retry;

            if (httpStatus >= 200 && httpStatus < 300)
            {
                if (responseBody != null &&
                    responseBody.Replace(" ", string.Empty).IndexOf("\"stored\":false", StringComparison.Ordinal) >= 0)
                {
                    return UploadOutcome.Retry;
                }
                return UploadOutcome.Delivered;
            }

            if (httpStatus == 400 || httpStatus == 413 || httpStatus == 422)
                return UploadOutcome.Rejected;

            return UploadOutcome.Retry;
        }
    }
}
