using NUnit.Framework;
using OzxApm.Network;

namespace OzxApm.Tests
{
    [TestFixture]
    public class EventReporterTests
    {
        [Test]
        public void Classify_2xx_IsDelivered()
        {
            Assert.AreEqual(UploadOutcome.Delivered,
                EventReporter.Classify(200, false, "{\"accepted\":3,\"rejected\":0,\"failed\":0,\"stored\":true}"));
            Assert.AreEqual(UploadOutcome.Delivered, EventReporter.Classify(202, false, null));
        }

        [Test]
        public void Classify_StoredFalse_IsRetried()
        {
            Assert.AreEqual(UploadOutcome.Retry,
                EventReporter.Classify(200, false, "{\"accepted\":1, \"failed\":2, \"stored\": false}"));
        }

        [Test]
        public void Classify_ServerStorageFailure_IsRetried()
        {
            Assert.AreEqual(UploadOutcome.Retry,
                EventReporter.Classify(503, false, "{\"accepted\":0,\"failed\":3,\"stored\":false}"));
        }

        [Test]
        public void Classify_TransientFailures_AreRetried()
        {
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(0, true, null)); // offline / DNS
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(500, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(502, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(429, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(408, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(401, false, null));
            Assert.AreEqual(UploadOutcome.Retry, EventReporter.Classify(404, false, null));
        }

        [Test]
        public void Classify_MalformedPayload_IsRejected()
        {
            Assert.AreEqual(UploadOutcome.Rejected, EventReporter.Classify(400, false, "invalid JSON"));
            Assert.AreEqual(UploadOutcome.Rejected, EventReporter.Classify(413, false, null));
        }

        [Test]
        public void Backoff_GrowsExponentially_AndIsCapped()
        {
            Assert.AreEqual(0f, EventReporter.ComputeBackoffSeconds(0, 5f, 300f, 0.5f));
            Assert.AreEqual(5f, EventReporter.ComputeBackoffSeconds(1, 5f, 300f, 0.5f), 0.001f);
            Assert.AreEqual(10f, EventReporter.ComputeBackoffSeconds(2, 5f, 300f, 0.5f), 0.001f);
            Assert.AreEqual(20f, EventReporter.ComputeBackoffSeconds(3, 5f, 300f, 0.5f), 0.001f);
            Assert.AreEqual(300f, EventReporter.ComputeBackoffSeconds(10, 5f, 300f, 0.5f), 0.001f);
            Assert.AreEqual(300f, EventReporter.ComputeBackoffSeconds(1000, 5f, 300f, 1f), 0.001f);
        }

        [Test]
        public void Backoff_HasJitter()
        {
            float low = EventReporter.ComputeBackoffSeconds(3, 5f, 300f, 0f);
            float high = EventReporter.ComputeBackoffSeconds(3, 5f, 300f, 1f);
            Assert.AreEqual(16f, low, 0.001f);
            Assert.AreEqual(24f, high, 0.001f);
        }

        [Test]
        public void Backoff_NeverGivesUp()
        {
            // There is no failure limit: even after many failures a retry is scheduled.
            for (int failures = 1; failures <= 500; failures++)
            {
                float delay = EventReporter.ComputeBackoffSeconds(failures, 5f, 300f, 0.5f);
                Assert.Greater(delay, 0f);
                Assert.LessOrEqual(delay, 300f);
            }
        }
    }
}
