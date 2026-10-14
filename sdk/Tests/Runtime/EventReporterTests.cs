using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;

namespace OzxApm.Tests
{
    [TestFixture]
    public class EventReporterClassifyTests
    {
        [Test]
        public void Classify_2xx_IsDelivered()
        {
            Assert.AreEqual(UploadOutcome.Delivered,
                EventReporter.Classify(200, false, "{\"accepted\":3,\"rejected\":0,\"failed\":0,\"stored\":true,\"retry\":false}"));
            Assert.AreEqual(UploadOutcome.Delivered, EventReporter.Classify(202, false, null));
            // Legacy-client semantics (200 + stored:false + retry:false) is still a final answer.
            Assert.AreEqual(UploadOutcome.Delivered, EventReporter.Classify(200, false, "{\"stored\":false,\"retry\":false}"));
        }

        [Test]
        public void Classify_RetryTrue_IsRetried()
        {
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(200, false, "{\"stored\": false, \"retry\" : true}"));
        }

        [Test]
        public void Classify_BareClientErrors_AreRetried()
        {
            // A 400 from a proxy or the gzip middleware says nothing about the data.
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(400, false, "invalid gzip content"));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(400, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(413, false, "<html>Request Entity Too Large</html>"));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(400, false, "{\"reject_code\":\"\"}"));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(401, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(404, false, null));
        }

        [Test]
        public void Classify_ExplicitReject_IsRejected()
        {
            Assert.AreEqual(UploadOutcome.Rejected,
                EventReporter.Classify(400, false, "{\"accepted\":0,\"reject_code\":\"invalid_json\",\"errors\":[\"x\"]}"));
            Assert.AreEqual(UploadOutcome.Rejected,
                EventReporter.Classify(413, false, "{\"reject_code\": \"payload_too_large\"}"));
            // Never on 429 or 5xx.
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(429, false, "{\"reject_code\":\"x\"}"));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(503, false, "{\"reject_code\":\"x\"}"));
        }

        [Test]
        public void Classify_FromBytes_HandlesWhitespaceAndCutOffBodies()
        {
            Func<string, byte[]> b = System.Text.Encoding.ASCII.GetBytes;
            var retry = b("{\"stored\":false,\"retry\" : true}");
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(200, false, retry, retry.Length));
            var cut = b("{\"retry\":tru"); // cut off
            Assert.AreEqual(UploadOutcome.Delivered, EventReporter.Classify(200, false, cut, cut.Length));
            var reject = b("{\"reject_code\":\"x\"}");
            Assert.AreEqual(UploadOutcome.Rejected, EventReporter.Classify(400, false, reject, reject.Length));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(400, false, reject, 0));
            // A length shorter than the buffer is honoured (pooled buffer with stale bytes).
            Assert.AreEqual(UploadOutcome.Delivered, EventReporter.Classify(200, false, retry, 10));
        }

        [Test]
        public void Classify_TransientFailures_AreRetried()
        {
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(0, true, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(500, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(503, false, "{\"stored\":false,\"retry\":true}"));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(429, false, null));
        }

        [Test]
        public void Backoff_IsFullJitter_AndCapped()
        {
            Assert.AreEqual(0f, EventReporter.ComputeBackoffSeconds(0, 5f, 300f, 0.5f));
            Assert.AreEqual(5f, EventReporter.ComputeBackoffSeconds(1, 5f, 300f, 1f), 0.001f);
            Assert.AreEqual(40f, EventReporter.ComputeBackoffSeconds(4, 5f, 300f, 1f), 0.001f);
            Assert.AreEqual(20f, EventReporter.ComputeBackoffSeconds(4, 5f, 300f, 0.5f), 0.001f);
            Assert.AreEqual(1f, EventReporter.ComputeBackoffSeconds(4, 5f, 300f, 0f), 0.001f); // floor
            Assert.AreEqual(300f, EventReporter.ComputeBackoffSeconds(1000, 5f, 300f, 1f), 0.001f);
        }

        [Test]
        public void UploadGap_ScalesWithRequestSize()
        {
            Assert.AreEqual(1f, EventReporter.ComputeUploadGapSeconds(900, 1f, 8192), 0.001f, "small requests: min interval");
            Assert.AreEqual(8f, EventReporter.ComputeUploadGapSeconds(64 * 1024, 1f, 8192), 0.001f, "64KB at 8KB/s");
            Assert.AreEqual(1f, EventReporter.ComputeUploadGapSeconds(64 * 1024, 1f, 0), 0.001f, "rate limit disabled");
        }

        [Test]
        public void Backoff_NeverGivesUp()
        {
            for (int failures = 1; failures <= 500; failures++)
            {
                float delay = EventReporter.ComputeBackoffSeconds(failures, 5f, 300f, 0.37f);
                Assert.Greater(delay, 0f);
                Assert.LessOrEqual(delay, 300f);
            }
        }

        [Test]
        public void RetryAfter_Parsing()
        {
            Assert.AreEqual(30f, EventReporter.ParseRetryAfter("30"));
            Assert.AreEqual(0f, EventReporter.ParseRetryAfter(null));
            Assert.AreEqual(0f, EventReporter.ParseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT"));
            Assert.AreEqual(3600f, EventReporter.ParseRetryAfter("999999"));
        }
    }

    /// <summary>
    /// The upload state machine, driven with a fake transport and clock against
    /// a real (synchronously pumped) OfflineStorage.
    /// </summary>
    [TestFixture]
    public class EventReporterStateMachineTests
    {
        private sealed class FakeRequest : IUploadRequest
        {
            public bool IsDone { get; set; }
            public long ResponseCode { get; set; }
            public bool TransportError { get; set; }
            public string Error { get; set; }
            public string ResponseBody { get; set; }
            public string RetryAfter { get; set; }
            public bool Aborted;
            public byte[] Payload;
            public bool Compressed;
            public bool Started;
            public void Start() { Started = true; }
            public void Abort() { Aborted = true; }
            public int CopyResponse(byte[] dst)
            {
                if (ResponseBody == null) return 0;
                var bytes = System.Text.Encoding.UTF8.GetBytes(ResponseBody);
                int n = Math.Min(dst.Length, bytes.Length);
                Array.Copy(bytes, dst, n);
                return n;
            }
            public void Dispose() { }

            public void Respond(long code, string body = null, string retryAfter = null)
            {
                ResponseCode = code;
                ResponseBody = body;
                RetryAfter = retryAfter;
                IsDone = true;
            }

            public void Fail()
            {
                TransportError = true;
                Error = "Cannot resolve destination host";
                IsDone = true;
            }
        }

        private sealed class FakeTransport : IUploadTransport
        {
            public readonly List<FakeRequest> Requests = new List<FakeRequest>();
            public string LastAppKey;

            public IUploadRequest Create(string url, byte[] payload, int length, bool compressed, string appKey, int timeoutSeconds)
            {
                LastAppKey = appKey;
                var copy = new byte[length]; // the caller's buffer is reused
                Array.Copy(payload, copy, length);
                var request = new FakeRequest { Payload = copy, Compressed = compressed };
                Requests.Add(request);
                return request;
            }
        }

        private string _dir;
        private ApmConfig _config;
        private OfflineStorage _storage;
        private FakeTransport _transport;
        private float _now;
        private NetworkReachability _reach;
        private EventReporter _reporter;

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_rep_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig
            {
                ServerUrl = "http://test",
                AppKey = "key",
                BatchSize = 1,
                MaxBatchBytes = 1024, // with ~900-byte events: one event per request, so requests are countable
                FlushIntervalSeconds = 3600f,
                EnableCompression = false,
                InitialUploadDelaySeconds = 10f,
                UploadIntervalSeconds = 1f,
                RetryBaseDelaySeconds = 5f,
                RetryMaxDelaySeconds = 300f,
                LogLevel = LogLevel.None
            };
            _storage = new OfflineStorage(_config, _dir, false);
            _transport = new FakeTransport();
            _now = 100f;
            _reach = NetworkReachability.ReachableViaLocalAreaNetwork;
            // Jitter fixed at its maximum so delays are deterministic: 5, 10, 20, ... 300.
            _reporter = new EventReporter(_config, _storage, null, _transport, () => _now, () => _reach, new FixedRandom(1.0));
        }

        private sealed class FixedRandom : System.Random
        {
            private readonly double _value;
            public FixedRandom(double value) { _value = value; }
            public override double NextDouble() => _value;
        }

        [TearDown]
        public void TearDown()
        {
            _storage.Shutdown(0);
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private void Enqueue(int n)
        {
            for (int i = 0; i < n; i++)
            {
                _storage.Enqueue(new PerfSampleEvent
                {
                    event_id = Guid.NewGuid().ToString("N"),
                    timestamp = 1700000000000,
                    app_version = new string('v', 800), // see MaxBatchBytes above
                    platform = "Android",
                    session_id = "s",
                    device_id = "d"
                });
            }
            _storage.Pump();
        }

        private void Step(float seconds = 0f)
        {
            _now += seconds;
            for (int i = 0; i < 3; i++)
                _reporter.Update(); // Idle -> Waiting -> Sending within one frame or two
            _storage.Pump();        // what the worker thread would do meanwhile
        }

        private int StoredSegments()
        {
            return Directory.Exists(_dir)
                ? Directory.GetFiles(_dir).Count(f => f.EndsWith(".seg") || f.EndsWith(".open"))
                : 0;
        }

        private FakeRequest Last => _transport.Requests.Last();

        [Test]
        public void NoUploadBeforeInitialDelay_ThenStarts()
        {
            Enqueue(1);
            Step();
            Step(5f);
            Assert.AreEqual(0, _transport.Requests.Count, "uploads wait for the game's loading");

            Step(5.1f);
            Assert.AreEqual(1, _transport.Requests.Count);
            Assert.AreEqual("key", _transport.LastAppKey);
        }

        [Test]
        public void NotifyGameReady_StartsUploadsEarly()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            Assert.AreEqual(1, _transport.Requests.Count);
        }

        [Test]
        public void Delivered_DeletesAndPacesTheDrain()
        {
            Enqueue(3);
            Assert.AreEqual(3, StoredSegments());
            _reporter.NotifyGameReady();

            Step();
            Last.Respond(200, "{\"stored\":true,\"retry\":false}");
            Step();
            Assert.AreEqual(2, StoredSegments());
            Assert.AreEqual(1, _transport.Requests.Count, "next request waits for UploadIntervalSeconds");

            Step(0.5f);
            Assert.AreEqual(1, _transport.Requests.Count);
            Step(0.6f);
            Assert.AreEqual(2, _transport.Requests.Count);
        }

        [Test]
        public void ServerFailure_KeepsData_HonoursRetryAfter()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            Last.Respond(503, "{\"stored\":false,\"retry\":true}", "30");
            Step();

            Assert.AreEqual(1, StoredSegments(), "data stays until the server confirms");
            Assert.AreEqual(1, _reporter.ConsecutiveFailures);
            Assert.GreaterOrEqual(_reporter.NextAttemptTime - _now, 30f - 0.01f);

            Step(29f);
            Assert.AreEqual(1, _transport.Requests.Count);
            Step(1.1f);
            Assert.AreEqual(2, _transport.Requests.Count, "retried after Retry-After");
            Last.Respond(200, "{}");
            Step();
            Assert.AreEqual(0, StoredSegments());
            Assert.AreEqual(0, _reporter.ConsecutiveFailures);
        }

        [Test]
        public void ManyFailures_NeverDiscard()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            for (int i = 0; i < 30; i++)
            {
                Step(400f); // past any backoff
                Assert.AreEqual(i + 1, _transport.Requests.Count);
                Last.Fail();
                Step();
                Assert.AreEqual(1, StoredSegments(), "attempt " + i);
                Assert.LessOrEqual(_reporter.NextAttemptTime - _now, 300f + 0.01f);
            }
        }

        [Test]
        public void Bare400_IsRetried_NotDeleted()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            Last.Respond(400, "invalid gzip content");
            Step();
            Assert.AreEqual(1, StoredSegments());
            Assert.IsFalse(Directory.Exists(Path.Combine(_dir, "quarantine")));
            Assert.AreEqual(1, _reporter.ConsecutiveFailures);
        }

        [Test]
        public void ExplicitReject_Quarantines_AndBacksOff()
        {
            Enqueue(2);
            _reporter.NotifyGameReady();
            Step();
            Last.Respond(400, "{\"reject_code\":\"invalid_json\"}");
            Step();

            Assert.AreEqual(1, StoredSegments(), "rejected batch left the upload queue");
            Assert.AreEqual(1, Directory.GetFiles(Path.Combine(_dir, "quarantine")).Length);
            Assert.Greater(_reporter.NextAttemptTime - _now, 0.9f, "a reject is followed by a backoff");
            Step(0.5f);
            Assert.AreEqual(1, _transport.Requests.Count);
        }

        [Test]
        public void NetworkComingBack_RetriesImmediately()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            Last.Fail();
            Step();          // failure 1: retry in 5s
            Step(5.1f);
            Assert.AreEqual(2, _transport.Requests.Count);
            Last.Fail();
            Step();          // failure 2: retry in 10s
            Assert.AreEqual(10f, _reporter.NextAttemptTime - _now, 0.01f);

            _reach = NetworkReachability.NotReachable;
            Step(1.1f);
            Assert.AreEqual(2, _transport.Requests.Count);
            _reach = NetworkReachability.ReachableViaCarrierDataNetwork;
            Step(1.1f);
            Assert.AreEqual(3, _transport.Requests.Count, "retried as soon as the network came back");
        }

        [Test]
        public void ConsentWithdrawnMidUpload_FinishesInFlight_StartsNothingNew()
        {
            Enqueue(2);
            _reporter.NotifyGameReady();
            Step();
            Assert.AreEqual(1, _transport.Requests.Count);

            _config.Consent = ConsentState.Denied;
            Last.Respond(200, "{}");
            Step();
            Assert.AreEqual(1, StoredSegments(), "the in-flight batch completed normally");

            Step(10f);
            Step(10f);
            Assert.AreEqual(1, _transport.Requests.Count, "no uploads without consent");

            _config.Consent = ConsentState.Granted;
            Step(1f);
            Assert.AreEqual(2, _transport.Requests.Count);
        }

        [Test]
        public void Shutdown_WithRequestInFlight_KeepsData()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            var inFlight = Last;

            _reporter.Shutdown();
            _storage.Shutdown(0);

            Assert.IsTrue(inFlight.Aborted);
            Assert.AreEqual(1, StoredSegments(), "aborted batch is sent again next launch");

            // Next launch picks it up.
            var storage = new OfflineStorage(_config, _dir, false);
            try
            {
                storage.RequestUpload();
                Assert.AreEqual(UploadPoll.Ready, storage.PollUpload(out var batch));
                CollectionAssert.AreEqual(inFlight.Payload, batch.Payload);
            }
            finally
            {
                storage.Shutdown(0);
            }
        }

        [Test]
        public void PauseWhileUploading_SealsAndKeepsEverything()
        {
            Enqueue(1);
            _reporter.NotifyGameReady();
            Step();
            Enqueue(1); // written to a new segment while the first is in flight
            _storage.Flush();
            Assert.IsTrue(_storage.WaitIdle(1000));
            Assert.AreEqual(2, StoredSegments());

            Last.Respond(200, "{}");
            Step();
            Assert.AreEqual(1, StoredSegments());
        }
    }
}
