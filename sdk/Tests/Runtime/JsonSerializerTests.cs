using System.Collections.Generic;
using System.Globalization;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    [TestFixture]
    public class JsonSerializerTests
    {
        private CultureInfo _savedCulture;

        [SetUp]
        public void SetUp()
        {
            _savedCulture = Thread.CurrentThread.CurrentCulture;
            // de-DE writes "59,50" by default, which used to produce invalid JSON.
            Thread.CurrentThread.CurrentCulture = new CultureInfo("de-DE");
        }

        [TearDown]
        public void TearDown()
        {
            Thread.CurrentThread.CurrentCulture = _savedCulture;
        }

        [Test]
        public void SerializeEvent_UsesInvariantDecimalPoint_UnderGermanCulture()
        {
            var evt = new PerfSampleEvent
            {
                fps = 59.5f,
                frame_time_ms = 16.75f,
                main_thread_ms = 1234.5f,
                gc_alloc_kb = -3.25f,
                mem_mb = 1024f,
                timestamp = 1700000000000,
                seq = 12345678901
            };

            string json = JsonSerializer.SerializeEvent(evt);

            StringAssert.Contains("\"fps\":59.50", json);
            StringAssert.Contains("\"frame_time_ms\":16.75", json);
            StringAssert.Contains("\"main_thread_ms\":1234.50", json); // no group separator
            StringAssert.Contains("\"gc_alloc_kb\":-3.25", json);
            StringAssert.Contains("\"seq\":12345678901", json);
            StringAssert.DoesNotContain("59,5", json);
        }

        [Test]
        public void SerializeEvent_RoundTripsThroughJsonUtility_UnderGermanCulture()
        {
            var evt = new PerfSampleEvent { fps = 42.25f, mem_mb = 512.5f, event_id = "abc", seq = 7, timestamp = 1 };

            string json = JsonSerializer.SerializeEvent(evt);
            var parsed = JsonUtility.FromJson<PerfSampleEvent>(json);

            Assert.AreEqual(42.25f, parsed.fps, 0.001f);
            Assert.AreEqual(512.5f, parsed.mem_mb, 0.001f);
            Assert.AreEqual("abc", parsed.event_id);
            Assert.AreEqual(7, parsed.seq);
        }

        [Test]
        public void SerializeEvent_IncludesEventIdAndSeq()
        {
            var evt = new ExceptionEvent
            {
                event_id = "0f8fad5bd9cb469fa16570867728950e",
                seq = 42,
                fingerprint = "fp",
                message = "boom",
                count = 1
            };

            string json = JsonSerializer.SerializeEvent(evt);

            StringAssert.Contains("\"event_id\":\"0f8fad5bd9cb469fa16570867728950e\"", json);
            StringAssert.Contains("\"seq\":42", json);
        }

        [Test]
        public void SerializeEvent_IsASingleLine()
        {
            var evt = new ExceptionEvent { message = "line1\nline2\r\n", stack = "at A\nat B", fingerprint = "f" };

            string json = JsonSerializer.SerializeEvent(evt);

            Assert.IsFalse(json.Contains("\n"), "events are stored one per line");
            Assert.IsFalse(json.Contains("\r"));
        }

        [Test]
        public void SerializeEvent_WritesNullForNonFiniteFloats()
        {
            var evt = new PerfSampleEvent { fps = float.NaN, frame_time_ms = float.PositiveInfinity };

            string json = JsonSerializer.SerializeEvent(evt);

            StringAssert.Contains("\"fps\":null", json);
            StringAssert.Contains("\"frame_time_ms\":null", json);
        }

        [Test]
        public void SerializeEventBatch_UsesInvariantCulture()
        {
            var events = new List<BaseEvent> { new JankEvent { duration_ms = 75.5f, max_frame_ms = 60.25f } };

            string json = JsonSerializer.SerializeEventBatch(events);

            StringAssert.Contains("\"duration_ms\":75.50", json);
            StringAssert.Contains("\"max_frame_ms\":60.25", json);
        }
    }
}
