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
                LowValueUploadIntervalSeconds = 0f, // no hold here; see the LowValueHold tests below
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

        // ---- Low-value upload interval (LowValueUploadIntervalSeconds) ----

        private DateTime _utc;

        /// <summary>Hold enabled, storage ages driven by the same fake clock as the reporter.</summary>
        private void EnableHold(float seconds, int batchSize = 5, int maxBatchBytes = 64 * 1024, float flushInterval = 30f)
        {
            _config.LowValueUploadIntervalSeconds = seconds;
            _config.BatchSize = batchSize;
            _config.MaxBatchBytes = maxBatchBytes;
            _config.FlushIntervalSeconds = flushInterval;
            _utc = new DateTime(2026, 1, 1, 0, 0, 0, DateTimeKind.Utc);
            _storage.UtcNowForTests = () => _utc;
        }

        private void HoldStep(float seconds = 0f)
        {
            _utc = _utc.AddSeconds(seconds);
            Step(seconds);
        }

        private PerfSampleEvent SmallPerf()
        {
            return new PerfSampleEvent
            {
                event_id = Guid.NewGuid().ToString("N"), timestamp = 1700000000000, app_version = "1",
                platform = "Android", session_id = "s", device_id = "d", fps = 30f
            };
        }

        private ExceptionEvent Exception(string tag)
        {
            return new ExceptionEvent
            {
                event_id = Guid.NewGuid().ToString("N"), timestamp = 1700000000000, app_version = "1",
                platform = "Android", session_id = "s", device_id = "d", fingerprint = tag, message = tag, count = 1
            };
        }

        private List<string> EnqueuePerf(int n)
        {
            var ids = new List<string>();
            for (int i = 0; i < n; i++)
            {
                var e = SmallPerf();
                ids.Add(e.event_id);
                _storage.Enqueue(e);
            }
            _storage.Pump();
            return ids;
        }

        private static string BodyOf(FakeRequest r) => System.Text.Encoding.UTF8.GetString(r.Payload);

        private static int EventCount(FakeRequest r)
        {
            string body = BodyOf(r);
            int n = 0, at = 0;
            while ((at = body.IndexOf("\"event_id\"", at, StringComparison.Ordinal)) >= 0) { n++; at++; }
            return n;
        }

        /// <summary>
        /// The first request of a process is never held (it goes out at the first
        /// upload opportunity). Tests of the steady-state hold start after it.
        /// </summary>
        private int FirstUploadDone()
        {
            _reporter.NotifyGameReady();
            EnqueuePerf(_config.BatchSize);
            HoldStep();
            Assert.AreEqual(1, _transport.Requests.Count, "first upload of the process");
            Last.Respond(200, "{}");
            HoldStep(1.1f);
            Assert.AreEqual(1, _transport.Requests.Count);
            return 1;
        }

        [Test]
        public void LowValueHold_FirstUploadAfterTheInitialDelay_SendsHeldLowValueData()
        {
            EnableHold(120f);
            var ids = EnqueuePerf(7); // one sealed (held) segment + 2 events in the open one
            HoldStep(5f);
            Assert.AreEqual(0, _transport.Requests.Count, "InitialUploadDelaySeconds (10 s) not over");

            HoldStep(5.1f);
            Assert.AreEqual(1, _transport.Requests.Count, "the first upload is not held for the interval");
            string body = BodyOf(Last);
            foreach (var id in ids)
                StringAssert.Contains(id, body, "held and open low-value data go out with the first request");
        }

        [Test]
        public void LowValueHold_FirstUploadAfterMarkTTI_SendsHeldLowValueData()
        {
            EnableHold(120f);
            var ids = EnqueuePerf(3); // open segment only
            _reporter.NotifyGameReady();
            HoldStep();
            Assert.AreEqual(1, _transport.Requests.Count);
            foreach (var id in ids)
                StringAssert.Contains(id, BodyOf(Last));
        }

        [Test]
        public void LowValueHold_FirstUploadWaitsForData_WhenNothingIsStoredYet()
        {
            EnableHold(120f);
            _reporter.NotifyGameReady();
            HoldStep(3f);
            Assert.AreEqual(0, _transport.Requests.Count, "nothing stored");

            var ids = EnqueuePerf(2); // open segment
            HoldStep(1.1f);
            Assert.AreEqual(1, _transport.Requests.Count, "the first request still goes out at the next opportunity");
            foreach (var id in ids)
                StringAssert.Contains(id, BodyOf(Last));
        }

        [Test]
        public void LowValueHold_AfterTheFirstUpload_LowValueDataIsHeldForTheIntervalAgain()
        {
            EnableHold(120f);
            EnqueuePerf(3);
            HoldStep(10.1f); // initial delay over: first request
            Assert.AreEqual(1, _transport.Requests.Count);
            Last.Respond(200, "{}");
            HoldStep(1.1f);

            var ids = EnqueuePerf(10); // two sealed segments
            HoldStep(60f);
            HoldStep(59f);
            Assert.AreEqual(1, _transport.Requests.Count, "held again after the first request");
            HoldStep(2f);
            Assert.AreEqual(2, _transport.Requests.Count, "released after the interval");
            foreach (var id in ids)
                StringAssert.Contains(id, BodyOf(Last));
        }

        [Test]
        public void LowValueHold_PerfSamplesWaitForTheInterval_ThenGoOutInOneRequest()
        {
            EnableHold(120f);
            int b = FirstUploadDone();

            var ids = new List<string>();
            for (int t = 0; t < 60; t++)
            {
                ids.AddRange(EnqueuePerf(1)); // one perf sample per second: a sealed 5-event segment every 5 s
                HoldStep(1f);
            }
            Assert.AreEqual(b, _transport.Requests.Count, "sealed perf segments are held back while idle");

            HoldStep(59f);
            Assert.AreEqual(b, _transport.Requests.Count, "still inside the 120 s interval");

            HoldStep(2f);
            Assert.AreEqual(b + 1, _transport.Requests.Count, "released when the oldest held segment is 120 s old");
            string body = BodyOf(Last);
            foreach (var id in ids)
                StringAssert.Contains(id, body, "all held segments go out together in one request");
        }

        [Test]
        public void LowValueHold_ExceptionsKeepTheirLatency_AndCarryHeldDataAlong()
        {
            // Same config with and without the hold: the exception must leave at the same time.
            foreach (float hold in new[] { 0f, 120f })
            {
                TearDown();
                SetUp();
                EnableHold(hold);
                int b = FirstUploadDone();

                var exc = Exception("boom");
                _storage.Enqueue(exc);
                var perf = EnqueuePerf(3); // open low-value segment, written in the same pump

                HoldStep(29f);
                Assert.AreEqual(b, _transport.Requests.Count, "hold=" + hold + ": exception segment not sealed yet (FlushIntervalSeconds)");
                HoldStep(1.5f);
                Assert.AreEqual(b + 1, _transport.Requests.Count, "hold=" + hold + ": exception sent after FlushIntervalSeconds, as before");
                string body = BodyOf(Last);
                StringAssert.Contains(exc.event_id, body);
                foreach (var id in perf)
                    StringAssert.Contains(id, body, "hold=" + hold + ": low-value data rides along with a request that goes out anyway");
            }
        }

        [Test]
        public void LowValueHold_HighValueSegmentsGoBeforeReleasedLowValueData()
        {
            // A full request of released perf data is waiting; an exception sealed
            // later must not queue behind it (and behind its size-based pause).
            EnableHold(120f, batchSize: 1, maxBatchBytes: 4096);
            Enqueue(6); // ~900-byte perf samples, one segment each: > MaxBatchBytes held, so released
            var exc = Exception("first");
            _storage.Enqueue(exc);
            _storage.Pump(); // BatchSize 1: the exception segment is sealed at once

            _reporter.NotifyGameReady();
            HoldStep();
            Assert.AreEqual(1, _transport.Requests.Count);
            StringAssert.Contains(exc.event_id, BodyOf(Last), "the exception goes out in the first request");
        }

        [Test]
        public void LowValueHold_ExceptionDoesNotWaitOutThePauseAfterALargePerfRequest()
        {
            // A large perf request is followed by a size-based pause
            // (bytes / UploadBytesPerSecond). An exception sealed during that pause
            // goes out after UploadIntervalSeconds, not after the pause.
            EnableHold(120f, batchSize: 1, maxBatchBytes: 8192);
            _config.UploadBytesPerSecond = 1024; // ~7 KB request -> ~7 s pause
            Enqueue(8);                          // ~900-byte perf samples: a full request
            _reporter.NotifyGameReady();
            HoldStep();
            Assert.AreEqual(1, _transport.Requests.Count);
            Last.Respond(200, "{}");
            HoldStep();
            Assert.Greater(_reporter.NextAttemptTime - _now, 5f, "large request: long pause for low-value data");

            var exc = Exception("during-pause");
            _storage.Enqueue(exc);
            _storage.Pump(); // BatchSize 1: sealed at once
            HoldStep(1.1f);
            Assert.AreEqual(2, _transport.Requests.Count, "the exception does not wait for the pause");
            StringAssert.Contains(exc.event_id, BodyOf(Last));
        }

        [Test]
        public void LowValueHold_FailureBackoffAlsoAppliesToExceptions()
        {
            EnableHold(120f, batchSize: 1);
            var exc = Exception("x");
            _storage.Enqueue(exc);
            _storage.Pump();
            _reporter.NotifyGameReady();
            HoldStep();
            Last.Respond(503);
            HoldStep();
            Assert.AreEqual(1, _transport.Requests.Count);
            HoldStep(1.1f);
            Assert.AreEqual(1, _transport.Requests.Count, "backoff (5 s) is not shortened for high-value data");
            HoldStep(4f);
            Assert.AreEqual(2, _transport.Requests.Count);
        }

        /// <summary>
        /// Drains a backlog left by an earlier launch and returns "seconds:events"
        /// per request.
        /// </summary>
        private List<string> BacklogDrainSchedule(float hold)
        {
            TearDown();
            SetUp();
            EnableHold(0f, batchSize: 1, maxBatchBytes: 4096);
            Enqueue(12); // earlier launch: 12 ~900-byte perf samples, one segment each
            _reporter.Shutdown();
            _storage.Shutdown(500);

            _config.LowValueUploadIntervalSeconds = hold;
            _storage = new OfflineStorage(_config, _dir, false);
            _storage.UtcNowForTests = () => _utc;
            _reporter = new EventReporter(_config, _storage, null, _transport, () => _now, () => _reach, new FixedRandom(1.0));
            float t0 = _now;
            _reporter.NotifyGameReady();

            var schedule = new List<string>();
            for (int i = 0; i < 60; i++)
            {
                int before = _transport.Requests.Count;
                HoldStep(0.5f);
                for (int r = before; r < _transport.Requests.Count; r++)
                {
                    schedule.Add((_now - t0).ToString("F1", System.Globalization.CultureInfo.InvariantCulture) + "s:" + EventCount(_transport.Requests[r]));
                    _transport.Requests[r].Respond(200, "{}");
                }
            }
            Assert.AreEqual(0, StoredSegments(), "hold=" + hold + ": backlog fully drained");
            return schedule;
        }

        [Test]
        public void LowValueHold_BacklogFromAnEarlierLaunch_DrainsExactlyAsWithoutTheHold()
        {
            var without = BacklogDrainSchedule(0f);
            var with = BacklogDrainSchedule(120f);
            Assert.GreaterOrEqual(without.Count, 3, "several paced requests");
            CollectionAssert.AreEqual(without, with, "same requests, same sizes, same pacing");
        }

        [Test]
        public void LowValueHold_PauseFlushesAndUploadsHeldDataAtOnce()
        {
            EnableHold(120f);
            int b = FirstUploadDone();
            var ids = EnqueuePerf(12);
            HoldStep(10f);
            Assert.AreEqual(b, _transport.Requests.Count);

            // What ApmClient.OnApplicationPause(true) does: flush, wait for the writer, retry now.
            _storage.Flush();
            Assert.IsTrue(_storage.WaitIdle(1000));
            _reporter.RetryNow();
            HoldStep();

            Assert.AreEqual(b + 1, _transport.Requests.Count, "pause does not wait for the interval");
            string body = BodyOf(Last);
            foreach (var id in ids)
                StringAssert.Contains(id, body, "including the open segment sealed by the flush");
        }

        [Test]
        public void LowValueHold_QuitKeepsHeldData_AndTheNextLaunchSendsItAtOnce()
        {
            EnableHold(120f);
            var ids = EnqueuePerf(7);
            HoldStep(5f);

            // What ApmClient does on quit: seal and stop.
            _reporter.Shutdown();
            _storage.Shutdown(500);

            // Next launch: segments from an earlier launch are never held.
            _storage = new OfflineStorage(_config, _dir, false);
            _reporter = new EventReporter(_config, _storage, null, _transport, () => _now, () => _reach, new FixedRandom(1.0));
            _storage.UtcNowForTests = () => _utc;
            _reporter.NotifyGameReady();
            HoldStep();

            Assert.AreEqual(1, _transport.Requests.Count);
            string body = BodyOf(Last);
            foreach (var id in ids)
                StringAssert.Contains(id, body);
        }

        [Test]
        public void LowValueHold_IntervalIsCappedAt600Seconds()
        {
            EnableHold(100000f);
            int b = FirstUploadDone();
            EnqueuePerf(5);
            HoldStep(599f);
            Assert.AreEqual(b, _transport.Requests.Count);
            HoldStep(2f);
            Assert.AreEqual(b + 1, _transport.Requests.Count, "documented maximum delay: 600 s");
        }

        [Test]
        public void LowValueHold_ZeroDisablesTheHold()
        {
            EnableHold(0f);
            _reporter.NotifyGameReady();
            EnqueuePerf(3); // open segment: without the hold it is not sealed early either
            HoldStep();
            Assert.AreEqual(0, _transport.Requests.Count, "0: old behaviour, the open segment waits for BatchSize");
            EnqueuePerf(2); // now a sealed segment
            HoldStep(1.1f);
            Assert.AreEqual(1, _transport.Requests.Count, "0: every sealed segment is sent right away (old cadence)");
        }
    }
}
