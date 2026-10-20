using System;
using System.Threading;
using UnityEngine;
using UnityEngine.Networking;

namespace OzxApm.Network
{
    /// <summary>One HTTP exchange of the session file uploader (APM-13).</summary>
    internal sealed class SessionHttpJob
    {
        public string Method;
        public string Url;
        /// <summary>Body bytes (JSON), or null.</summary>
        public byte[] Body;
        /// <summary>A file sent as the body (a part), or null.</summary>
        public string BodyFile;
        public string ContentType;
        /// <summary>Header names and values, alternating.</summary>
        public string[] Headers;
        public int TimeoutSeconds;

        // Result (written by the main thread, read by the worker after Done).
        public long Status;
        public bool TransportError;
        public string ResponseText;
        public string RetryAfter;
        public string Error;
        public readonly ManualResetEvent Done = new ManualResetEvent(false);
    }

    /// <summary>A started request.</summary>
    internal interface ISessionHttpRequest : IDisposable
    {
        bool IsDone { get; }
        void Complete(SessionHttpJob job);
        void Abort();
    }

    /// <summary>Starts session file requests (UnityWebRequest by default; a fake in tests).</summary>
    internal interface ISessionHttpTransport
    {
        ISessionHttpRequest Start(SessionHttpJob job);
    }

    /// <summary>
    /// Runs the uploader's HTTP requests on the main thread, where
    /// UnityWebRequest must be created: the worker thread posts a job and waits;
    /// <see cref="Update"/> starts it when the technical and ops uploaders are
    /// not sending (a big part never delays their requests; held at most
    /// <see cref="MaxHoldSeconds"/>), polls it, and hands the result back.
    /// Per frame while idle: one volatile read, no allocation.
    /// </summary>
    internal sealed class SessionFileHttp
    {
        internal const float MaxHoldSeconds = 30f;

        private readonly ISessionHttpTransport _transport;
        // Handed over with Interlocked: posted by the worker, taken by Update, or withdrawn.
        private SessionHttpJob _posted;
        private SessionHttpJob _active;
        private ISessionHttpRequest _request;
        private float _heldSince = -1f;
        private volatile bool _closed;

        public SessionFileHttp(ISessionHttpTransport transport)
        {
            _transport = transport;
        }

        /// <summary>A request is posted or running.</summary>
        public bool Busy => Volatile.Read(ref _posted) != null || _active != null;

        /// <summary>Main thread, every frame. othersSending: the event or ops uploader has a request under way.</summary>
        public void Update(float now, bool othersSending)
        {
            if (_active != null)
            {
                if (_request.IsDone)
                    Finish(null);
                return;
            }
            var job = Volatile.Read(ref _posted);
            if (job == null)
                return;
            if (othersSending)
            {
                if (_heldSince < 0f)
                    _heldSince = now;
                if (now - _heldSince < MaxHoldSeconds)
                    return;
            }
            _heldSince = -1f;
            if (Interlocked.CompareExchange(ref _posted, null, job) != job)
                return; // withdrawn by the worker meanwhile
            _active = job;
            try
            {
                _request = _transport.Start(job);
            }
            catch (Exception ex)
            {
                _request = null;
                Finish(ex.Message);
            }
        }

        private void Finish(string startError)
        {
            var job = _active;
            var request = _request;
            _active = null;
            _request = null;
            try
            {
                if (request != null)
                    request.Complete(job);
                else
                {
                    job.TransportError = true;
                    job.Error = startError;
                }
            }
            catch (Exception ex)
            {
                job.TransportError = true;
                job.Error = ex.Message;
            }
            finally
            {
                try { request?.Dispose(); } catch { }
                try
                {
                    job.Done.Set();
                }
                catch (ObjectDisposedException)
                {
                    // The worker gave up waiting for this one.
                }
            }
        }

        /// <summary>
        /// Worker thread: posts the job and waits for its result (false: closed,
        /// or no result within the timeout: the job is abandoned).
        /// </summary>
        public bool Send(SessionHttpJob job, int waitMs)
        {
            if (_closed)
                return false;
            Volatile.Write(ref _posted, job);
            bool done = job.Done.WaitOne(waitMs);
            if (!done)
            {
                // Not started yet: withdraw it (the main thread may be paused);
                // if Update took it just now, its result is simply not read.
                Interlocked.CompareExchange(ref _posted, null, job);
            }
            return done && !_closed;
        }

        /// <summary>Main thread: aborts the running request and refuses new ones until Reopen.</summary>
        public void Close()
        {
            _closed = true;
            var posted = Interlocked.Exchange(ref _posted, null);
            if (posted != null)
            {
                posted.TransportError = true;
                posted.Error = "closed";
                try
                {
                    posted.Done.Set();
                }
                catch (ObjectDisposedException)
                {
                }
            }
            if (_active != null)
            {
                try { _request?.Abort(); } catch { }
                Finish("aborted");
            }
        }

        public void Reopen() => _closed = false;
    }

    /// <summary>UnityWebRequest transport: a part is sent from its file (UploadHandlerFile, no managed copy).</summary>
    internal sealed class UnityWebRequestSessionTransport : ISessionHttpTransport
    {
        public ISessionHttpRequest Start(SessionHttpJob job)
        {
            var request = new UnityWebRequest(job.Url, job.Method) { timeout = Math.Max(1, job.TimeoutSeconds) };
            try
            {
                if (job.BodyFile != null)
                    request.uploadHandler = new UploadHandlerFile(job.BodyFile);
                else if (job.Body != null)
                    request.uploadHandler = new UploadHandlerRaw(job.Body);
                request.downloadHandler = new DownloadHandlerBuffer();
                if (job.ContentType != null)
                    request.SetRequestHeader("Content-Type", job.ContentType);
                if (job.Headers != null)
                {
                    for (int i = 0; i + 1 < job.Headers.Length; i += 2)
                    {
                        if (!string.IsNullOrEmpty(job.Headers[i + 1]))
                            request.SetRequestHeader(job.Headers[i], job.Headers[i + 1]);
                    }
                }
                request.SendWebRequest();
                return new Request(request);
            }
            catch
            {
                request.Dispose();
                throw;
            }
        }

        private sealed class Request : ISessionHttpRequest
        {
            private readonly UnityWebRequest _r;

            public Request(UnityWebRequest r) => _r = r;
            public bool IsDone => _r.isDone;

            public void Complete(SessionHttpJob job)
            {
                job.Status = _r.responseCode;
                job.TransportError = _r.result == UnityWebRequest.Result.ConnectionError;
                job.Error = _r.error;
                job.RetryAfter = _r.GetResponseHeader("Retry-After");
                job.ResponseText = _r.downloadHandler != null ? _r.downloadHandler.text : null;
            }

            public void Abort() => _r.Abort();
            public void Dispose() => _r.Dispose();
        }
    }
}
