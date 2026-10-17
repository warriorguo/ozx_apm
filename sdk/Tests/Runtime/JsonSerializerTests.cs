using System.Collections.Generic;
using System.Globalization;
using System.Text;
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

        // APM-4 fields: written when set, absent otherwise (what older SDKs send).
        [Test]
        public void SerializeEvent_ExceptionDetails()
        {
            var evt = new ExceptionEvent
            {
                fingerprint = "f", message = "m", count = 1, log_type = "error",
                embedded_exception = "System.Exception: \"x\"\n  at A.B () [0x0] in <z>:0",
                breadcrumbs = new List<string> { "[10:00:00.000] Scene: Main", "a \"quoted\" one" }
            };
            string json = JsonSerializer.SerializeEvent(evt);
            StringAssert.Contains("\"log_type\":\"error\"", json);
            StringAssert.Contains("\"embedded_exception\":\"System.Exception: \\\"x\\\"\\n  at A.B () [0x0] in <z>:0\"", json);
            StringAssert.Contains("\"breadcrumbs\":[\"[10:00:00.000] Scene: Main\",\"a \\\"quoted\\\" one\"]", json);

            string bare = JsonSerializer.SerializeEvent(new ExceptionEvent { fingerprint = "f", message = "m", count = 2 });
            StringAssert.DoesNotContain("log_type", bare);
            StringAssert.DoesNotContain("embedded_exception", bare);
            StringAssert.DoesNotContain("breadcrumbs", bare);
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

        // APM-3: floats are formatted without allocating, digit for digit as
        // float.ToString("F2", InvariantCulture) on this runtime's class library.
        [Test]
        public void Float2_MatchesToStringF2()
        {
            var values = new List<float>
            {
                0f, -0f, 0.001f, -0.004f, 0.005f, 0.015f, 0.025f, 0.0149999959f, 0.0249999985f, 0.03125f, 0.125f, 1.005f,
                59.5f, 59.995f, 99.995f, -1.995f, 150109.25f, 2071466.5f, 16777217f, 9999999.5f, 1e14f, 999999999999999f
            };
            var random = new System.Random(3);
            var bits = new byte[4];
            while (values.Count < 200000)
            {
                random.NextBytes(bits);
                float v = System.BitConverter.ToSingle(bits, 0);
                if (!float.IsNaN(v) && !float.IsInfinity(v) && System.Math.Abs(v) < 1e15f)
                    values.Add(v);
                values.Add((float)(random.NextDouble() * 240));
                values.Add(random.Next(-100000, 100000) / 200f);
            }

            var line = new JsonLine();
            int mismatches = 0;
            string first = null;
            foreach (float v in values)
            {
                line.Clear();
                line.Float2(v);
                string mine = Encoding.ASCII.GetString(line.Buffer, 0, line.Length);
                string expected = v.ToString("F2", CultureInfo.InvariantCulture);
                if (mine != expected)
                {
                    mismatches++;
                    first = first ?? v.ToString("R", CultureInfo.InvariantCulture) + ": " + mine + " != " + expected;
                }
            }
            Assert.AreEqual(0, mismatches, first);
        }

        // The worker writes UTF-8 bytes directly; they are what Encoding.UTF8
        // makes of the old string (a lone surrogate becomes U+FFFD in both).
        [Test]
        public void WriteEvent_Utf8_MatchesEncodingOfTheText()
        {
            // e-acute, u-umlaut, CJK, an emoji (surrogate pair), escapes, controls, DEL, U+2028, lone surrogates.
            string text = "\u00e9 \u00fc \u4e2d\u6587 \U0001F600 \"q\" \\ \t\n\r \u0001 \u007f " + (char)0x2028 + " lone:" + (char)0xD800 + " end:" + (char)0xDC00;
            var evt = new ExceptionEvent { fingerprint = "f", message = text, stack = text, count = 1, event_id = "e" };
            var line = new JsonLine(16); // grows while writing
            JsonSerializer.WriteEvent(line, evt);
            var bytes = new byte[line.Length];
            System.Buffer.BlockCopy(line.Buffer, 0, bytes, 0, line.Length);

            string json = Encoding.UTF8.GetString(bytes);
            CollectionAssert.AreEqual(Encoding.UTF8.GetBytes(json), bytes);
            string expected = "\"message\":\"\u00e9 \u00fc \u4e2d\u6587 \U0001F600 \\\"q\\\" \\\\ \\t\\n\\r \\u0001 \u007f " + (char)0x2028 + " lone:" + (char)0xFFFD + " end:" + (char)0xFFFD + "\"";
            StringAssert.Contains(expected, json);
        }

        [Test]
        public void WriteEvent_WorkerAssignedId_IsWrittenAs32HexDigits()
        {
            var evt = new PerfSampleEvent { id_hi = 0x0123456789abcdefUL, id_lo = 0xfedcba9876543210UL };
            StringAssert.Contains("\"event_id\":\"0123456789abcdeffedcba9876543210\"", JsonSerializer.SerializeEvent(evt));
            evt.event_id = "given";
            StringAssert.Contains("\"event_id\":\"given\"", JsonSerializer.SerializeEvent(evt), "an id set by the caller wins");
        }

        [Test]
        public void WriteEvent_LongAndContext()
        {
            var evt = new PerfSampleEvent { seq = long.MaxValue, timestamp = -1 };
            string json = JsonSerializer.SerializeEvent(evt);
            StringAssert.Contains("\"seq\":9223372036854775807", json);
            StringAssert.Contains("\"timestamp\":-1", json);

            OzxApm.Core.GameContext.ResetForTests();
            try
            {
                OzxApm.Core.GameContext.Set("roomId", "r-\"12\"");
                evt.ctx = OzxApm.Core.GameContext.Current;
                StringAssert.EndsWith(",\"ctx\":{\"roomId\":\"r-\\\"12\\\"\"}}", JsonSerializer.SerializeEvent(evt));
            }
            finally
            {
                OzxApm.Core.GameContext.ResetForTests();
            }
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
