using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using NUnit.Framework;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-7: the ANR watchdog (a marker on the main looper; Android's
    /// NOT_RESPONDING state confirms), its live report, and the next launch's
    /// report of the same ANR from the exit record (same event id).
    /// </summary>
    [TestFixture]
    public class AnrWatchdogTests
    {
        internal sealed class FakeProbe : IMainThreadProbe
        {
            public bool Blocked;          // the main thread does not run the marker
            public bool NotResponding;    // Android's error state
            public bool Queued;
            public int Posts, Cancels, ErrorStatePolls, Stacks;

            public bool Start() => true;
            public void Stop() { }

            public bool Post()
            {
                Posts++;
                Queued = true;
                return true;
            }

            public bool IsPending()
            {
                if (!Blocked)
                    Queued = false; // the main thread ran it
                return Queued;
            }

            public void Cancel()
            {
                Cancels++;
                Queued = false;
            }

            public bool IsNotResponding(out string message)
            {
                ErrorStatePolls++;
                message = NotResponding ? "Input dispatching timed out" : "";
                return NotResponding;
            }

            public string MainThreadStack(int maxFrames)
            {
                Stacks++;
                return "Thread \"main\" tid=4796 crashed\nat java.lang.Thread.sleep(Native method)\nat com.ozx.apmtest.Blocker.block(Blocker.java:25)\n";
            }
        }

        private FakeProbe _probe;
        private bool _active;
        private List<(string message, string stack)> _anrs;
        private AnrWatchdog _w;

        [SetUp]
        public void SetUp()
        {
            _probe = new FakeProbe();
            _active = true;
            _anrs = new List<(string, string)>();
            _w = new AnrWatchdog(_probe, () => _active, (m, s) => _anrs.Add((m, s)));
        }

        private void Run(long fromMs, long toMs)
        {
            for (long t = fromMs; t <= toMs; t += AnrWatchdog.TickMs)
                _w.Tick(t);
        }

        [Test]
        public void ResponsiveMainThread_NothingButMarkers()
        {
            Run(0, 60_000);
            Assert.AreEqual(0, _w.Blocks);
            Assert.AreEqual(0, _anrs.Count);
            Assert.AreEqual(0, _probe.ErrorStatePolls, "the error state is only polled during a block");
            Assert.That(_probe.Posts, Is.InRange(15, 25), "about one marker every 3 s");
        }

        [Test]
        public void SlowMainThreadWithoutAnAndroidAnr_IsNotReported()
        {
            Run(0, 2000);
            _probe.Blocked = true;
            Run(3000, 3000 + AnrWatchdog.BlockedMs + AnrWatchdog.ConfirmForMs + 10_000);
            Assert.AreEqual(1, _w.Blocks);
            Assert.AreEqual(0, _anrs.Count);
            Assert.LessOrEqual(_probe.ErrorStatePolls, (int)(AnrWatchdog.ConfirmForMs / AnrWatchdog.TickMs) + 2, "polled for a bounded time only");
        }

        [Test]
        public void AnrConfirmedByAndroid_ReportedOncePerBlock()
        {
            Run(0, 2000);
            _probe.Blocked = true;
            Run(3000, 9000);
            Assert.AreEqual(0, _anrs.Count, "not yet declared by Android");
            _probe.NotResponding = true;
            Run(10_000, 40_000);
            Assert.AreEqual(1, _anrs.Count);
            Assert.AreEqual("Input dispatching timed out", _anrs[0].message);
            StringAssert.StartsWith("Thread \"main\" tid=4796 crashed\nat java.lang.Thread.sleep", _anrs[0].stack);
            Assert.AreEqual(1, _probe.Stacks);

            // It recovers, then blocks again: a second ANR.
            _probe.Blocked = false;
            _probe.NotResponding = false;
            Run(41_000, 50_000);
            _probe.Blocked = true;
            _probe.NotResponding = true;
            Run(51_000, 70_000);
            Assert.AreEqual(2, _anrs.Count);
            Assert.AreEqual(2, _w.Blocks);
        }

        [Test]
        public void AtMostMaxReportsPerProcess()
        {
            for (int i = 0; i < AnrWatchdog.MaxReportsPerProcess + 3; i++)
            {
                long t0 = i * 100_000L;
                _probe.Blocked = false;
                _probe.NotResponding = false;
                Run(t0, t0 + 5000);
                _probe.Blocked = true;
                _probe.NotResponding = true;
                Run(t0 + 6000, t0 + 30_000);
            }
            Assert.AreEqual(AnrWatchdog.MaxReportsPerProcess, _anrs.Count);
        }

        [Test]
        public void Paused_CancelsTheMarkerAndWatchesNothing()
        {
            Run(0, 2000);
            _probe.Blocked = true;
            Run(3000, 4000);
            _active = false;
            _probe.NotResponding = true;
            int posts = _probe.Posts;
            Run(5000, 60_000);
            Assert.AreEqual(posts, _probe.Posts);
            Assert.AreEqual(1, _probe.Cancels);
            Assert.AreEqual(0, _anrs.Count, "in the background the watchdog does not report");
        }

        [Test]
        public void Tick_DoesNotAllocate()
        {
            // Steady state, responsive and blocked (before Android declares an ANR):
            // the same loop once to warm it up, then measured.
            long t = 0;
            long Measure(int ticks)
            {
                long before = GC.GetAllocatedBytesForCurrentThread();
                for (int i = 0; i < ticks; i++, t += AnrWatchdog.TickMs)
                    _w.Tick(t);
                return GC.GetAllocatedBytesForCurrentThread() - before;
            }
            Measure(100);
            Measure(100);
            Assert.AreEqual(0, Measure(1000), "responsive");
            _probe.Blocked = true;
            Measure(100);
            Assert.AreEqual(0, Measure(1000), "blocked, not declared");
        }

        // ------------------------------------------------------------------
        // Through the CrashMonitor: live report, then the exit record's trace
        // ------------------------------------------------------------------

        [Test]
        public void LiveAnr_IsReportedWithTheSession_AndTheExitTraceOfItIsTheSameEvent()
        {
            string dir = Path.Combine(Path.GetTempPath(), "ozx_apm_anr_" + Guid.NewGuid().ToString("N"));
            GameContext.ResetForTests();
            try
            {
                var clock = new SessionClock();
                clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 60_000, "1760000000000_anr00001");
                var timeline = new Timeline();
                timeline.RecordText("Scene: Main");
                GameContext.Set("roomId", "r-anr");
                var inputs = new JournalInputs
                {
                    Pid = 4796,
                    ProcessStartMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 60_000,
                    AppVersion = "2.1.0",
                    Platform = "Android",
                    Clock = clock,
                    DeviceId = () => "0123456789abcdef0123456789abcdef",
                    Timeline = timeline,
                };
                var live = new List<CrashEvent>();
                var monitor = new CrashMonitor(dir, inputs, null, live.Add, () => true, ms => true, false) { Probe = _probe };
                monitor.OnCollecting(true);
                for (long t = 0; t <= 2000; t += 1000)
                    monitor.TickWatchdogForTests(t);
                _probe.Blocked = true;
                _probe.NotResponding = true;
                for (long t = 3000; t <= 12_000; t += 1000)
                    monitor.TickWatchdogForTests(t);

                Assert.AreEqual(1, live.Count);
                var e = live[0];
                Assert.AreEqual("anr", e.crash_type);
                Assert.AreEqual("1760000000000_anr00001", e.session_id);
                Assert.AreEqual("2.1.0", e.app_version);
                Assert.IsTrue(e.foreground);
                Assert.IsTrue(e.ctx.TryGetValue("roomId", out var room) && room == "r-anr");
                StringAssert.Contains("Scene: Main", string.Join("\n", e.breadcrumbs));
                StringAssert.StartsWith("Thread \"main\" tid=4796 crashed\n", e.stack);
                var d = Dict(e.details);
                Assert.AreEqual("watchdog", d["detected_by"]);
                Assert.AreEqual("watchdog", d["trace"]);
                Assert.AreEqual("Input dispatching timed out", d["cause"]);
                Assert.IsNull(e.exit_reason);

                // The journal remembers it; the game quits cleanly.
                monitor.Journal.WriteNow();
                monitor.OnShutdown();
                var journal = Directory.GetFiles(dir, CrashJournal.FilePrefix + "*").Select(File.ReadAllText).First(x => x.Contains("clean=1"));
                StringAssert.Contains("anr_id=" + e.event_id, journal);

                // Next launch: the SIGNALED exit carries the ANR trace of the same ANR.
                var exits = new TraceExits(4796, CrashTraceParserTests.AnrTraceOfASurvivedAnr(), e.timestamp + 90_000, e.timestamp - 2000);
                var next = new List<CrashEvent>();
                new CrashReporter(dir, exits, next.Add, () => true, ms => true).ProcessPrevious(1, 1);
                Assert.AreEqual(1, next.Count);
                Assert.AreEqual("anr", next[0].crash_type);
                Assert.AreEqual(e.event_id, next[0].event_id, "the same ANR: the server drops the duplicate");
            }
            finally
            {
                GameContext.ResetForTests();
                try
                {
                    Directory.Delete(dir, true);
                }
                catch
                {
                    // Best effort.
                }
            }
        }

        // An exit record with an ANR trace whose dump time is anrMs.
        private sealed class TraceExits : IExitInfoSource
        {
            private readonly int _pid;
            private readonly byte[] _trace;
            private readonly long _exitMs;
            private readonly long _anrMs;

            public TraceExits(int pid, byte[] trace, long exitMs, long anrMs)
            {
                _pid = pid;
                _trace = trace;
                _exitMs = exitMs;
                _anrMs = anrMs;
            }

            public List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir)
            {
                var r = new ExitRecord { Pid = _pid, TimestampMs = _exitMs, Reason = ExitReasons.Signaled, Status = 9, Importance = 400 };
                // The fixture's dump time replaced by anrMs (local time with its offset).
                string text = System.Text.Encoding.UTF8.GetString(_trace).Replace("2026-10-19 21:44:39.650209283+0800",
                    DateTimeOffset.FromUnixTimeMilliseconds(_anrMs).ToString("yyyy-MM-dd HH:mm:ss.fff", System.Globalization.CultureInfo.InvariantCulture) + "+0000");
                r.TracePath = Path.Combine(traceDir, CrashReporter.TracePrefix + _pid + ".bin");
                File.WriteAllText(r.TracePath, text);
                return new List<ExitRecord> { r };
            }
        }

        private static Dictionary<string, string> Dict(string[] pairs)
        {
            var d = new Dictionary<string, string>();
            for (int i = 0; i + 1 < pairs.Length; i += 2)
                d[pairs[i]] = pairs[i + 1];
            return d;
        }
    }
}
