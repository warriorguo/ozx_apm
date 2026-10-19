using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using NUnit.Framework;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-9 without the Unity runtime: /proc/self/statm parsing and reading,
    /// the run tracker (peaks, start/end, a new run ending the open one), the
    /// count ranges of SetContextCount, the memory sampler with a fake engine
    /// source, the crash journal's memory and run lines, the crash's details
    /// and the run reported at the next launch, and the wire format.
    /// </summary>
    [TestFixture]
    public class MemoryRunTests
    {
        private string _dir;

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_mem_test_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_dir);
            GameContext.ResetForTests();
        }

        [TearDown]
        public void TearDown()
        {
            GameContext.ResetForTests();
            try
            {
                Directory.Delete(_dir, true);
            }
            catch
            {
                // Best effort.
            }
        }

        // ------------------------------------------------------------------
        // /proc/self/statm
        // ------------------------------------------------------------------

        private static long Parse(string s)
        {
            var b = Encoding.ASCII.GetBytes(s);
            return ProcStatm.ParseResidentPages(b, b.Length);
        }

        [Test]
        public void Statm_ResidentPagesIsTheSecondNumber()
        {
            Assert.AreEqual(98765, Parse("1803420 98765 40213 5 0 371234 0\n"));
            Assert.AreEqual(7, Parse("  12 7"));
            Assert.AreEqual(-1, Parse(""));
            Assert.AreEqual(-1, Parse("12"));
            Assert.AreEqual(-1, Parse("abc 12"));
            Assert.AreEqual(-1, Parse("12 x"));
            Assert.AreEqual(-1, Parse("1 99999999999999999999"));
        }

        [Test]
        public void Statm_ReadsAndReReadsTheFile()
        {
            string path = Path.Combine(_dir, "statm");
            File.WriteAllText(path, "1000 250 10 1 0 300 0\n");
            using (var statm = new ProcStatm(path, 4096))
            {
                Assert.AreEqual(250L * 4096, statm.ReadRssBytes());
                File.WriteAllText(path, "1000 300 10 1 0 300 0\n");
                Assert.AreEqual(300L * 4096, statm.ReadRssBytes());
                Assert.IsFalse(statm.Disabled);
            }
            using (var missing = new ProcStatm(Path.Combine(_dir, "none"), 4096))
            {
                for (int i = 0; i < 5; i++)
                    Assert.AreEqual(-1, missing.ReadRssBytes());
                Assert.IsTrue(missing.Disabled);
            }
        }

        // ------------------------------------------------------------------
        // Count ranges (SetContextCount): the server's models.CountBucketLabels
        // ------------------------------------------------------------------

        [Test]
        public void CountBucket_MatchesTheServersRanges()
        {
            var cases = new Dictionary<long, string>
            {
                { -3, "0" }, { 0, "0" }, { 1, "1-5" }, { 5, "1-5" }, { 6, "6-10" }, { 10, "6-10" }, { 11, "11-20" },
                { 20, "11-20" }, { 21, "21-30" }, { 31, "31-50" }, { 50, "31-50" }, { 51, "51-100" }, { 100, "51-100" },
                { 101, "101-200" }, { 200, "101-200" }, { 201, "201+" }, { long.MaxValue, "201+" },
            };
            foreach (var c in cases)
                Assert.AreEqual(c.Value, GameContext.CountBucket(c.Key), c.Key.ToString(CultureInfo.InvariantCulture));
            // Constant strings: the same reference every time (no allocation, the context only changes between ranges).
            Assert.AreSame(GameContext.CountBucket(7), GameContext.CountBucket(9));
        }

        // ------------------------------------------------------------------
        // RunTracker
        // ------------------------------------------------------------------

        private static MemoryFigures Fig(float managed, float alloc, float rss, float gfx = 0) =>
            new MemoryFigures { Managed = managed, MonoHeap = managed + 10, Alloc = alloc, Reserved = alloc + 100, Gfx = gfx, Rss = rss };

        [Test]
        public void Runs_PeaksStartAndEnd()
        {
            var t = new RunTracker();
            Assert.IsNull(t.End(RunEvent.EndEnd, null), "no run open");
            t.Record(Fig(50, 300, 900), true);
            Assert.IsNull(t.Begin("run-1"));
            Assert.AreEqual("run-1", t.CurrentRunId);
            t.Record(Fig(80, 500, 1200, 64), true);
            t.Record(Fig(60, 450, 1100), true);
            t.RecordRss(1500); // the reader thread saw a higher RSS between samples
            var e = t.End(RunEvent.EndEnd, "win");
            Assert.IsNull(t.CurrentRunId);
            Assert.AreEqual("run-1", e.run_id);
            Assert.AreEqual("end", e.end_reason);
            Assert.AreEqual("win", e.result);
            Assert.AreEqual(2, e.samples);
            Assert.AreEqual(80, e.peak_mem_mb);
            Assert.AreEqual(90, e.peak_mono_heap_mb);
            Assert.AreEqual(500, e.peak_alloc_mb);
            Assert.AreEqual(600, e.peak_reserved_mb);
            Assert.AreEqual(64, e.peak_gfx_mb);
            Assert.AreEqual(1500, e.peak_rss_mb);
            Assert.AreEqual(300, e.start_alloc_mb, "the reading before BeginRun");
            Assert.AreEqual(900, e.start_rss_mb);
            Assert.AreEqual(450, e.end_alloc_mb);
            Assert.AreEqual(1500, e.end_rss_mb);
            Assert.GreaterOrEqual(e.duration_ms, 0);
            Assert.AreEqual(1, t.RunsEnded);
        }

        [Test]
        public void Runs_BeginWhileOpenEndsThePreviousRun()
        {
            var t = new RunTracker();
            t.Begin("a");
            t.Record(Fig(10, 2000, 3000), true);
            var previous = t.Begin("b");
            Assert.AreEqual("a", previous.run_id);
            Assert.AreEqual("new_run", previous.end_reason);
            Assert.AreEqual(3000, previous.peak_rss_mb);
            t.Record(Fig(10, 100, 700), true);
            var b = t.End(RunEvent.EndShutdown, null);
            Assert.AreEqual("b", b.run_id);
            // b's peak starts from the last reading when it began (a's figures), not from a's peak.
            Assert.AreEqual(3000, b.peak_rss_mb);
            Assert.AreEqual(1, b.samples);
            Assert.AreEqual("shutdown", b.end_reason);
        }

        [Test]
        public void Runs_RssFromTheReaderIsNotOverwrittenByTheSample()
        {
            var t = new RunTracker();
            t.Begin("r");
            t.RecordRss(1800);
            t.Record(Fig(10, 100, 0), false); // the perf sample does not read RSS where the reader does
            t.Read(out var s);
            Assert.AreEqual(1800, s.Last.Rss);
            Assert.AreEqual(1800, s.RunPeak.Rss);
            Assert.AreEqual(1800, s.ProcessPeakRss);
            Assert.AreEqual("r", s.RunId);
        }

        [Test]
        public void Runs_JournalIsRewrittenWhenMemoryMovesBy32Mb()
        {
            var t = new RunTracker();
            Assert.IsTrue(t.RecordRss(500), "first reading");
            Assert.IsFalse(t.RecordRss(520));
            Assert.IsTrue(t.RecordRss(533));
            Assert.IsFalse(t.RecordRss(540));
            Assert.IsTrue(t.Record(Fig(1, 100, 0), false), "native memory moved");
        }

        [Test]
        public void RunIds()
        {
            Assert.IsTrue(RunTracker.IsValidRunId("42"));
            Assert.IsTrue(RunTracker.IsValidRunId("run 7 / seed 991 ü"));
            Assert.IsFalse(RunTracker.IsValidRunId(null));
            Assert.IsFalse(RunTracker.IsValidRunId(""));
            Assert.IsFalse(RunTracker.IsValidRunId("a\nb"));
            Assert.IsFalse(RunTracker.IsValidRunId(new string('x', 65)));
        }

        // ------------------------------------------------------------------
        // MemorySampler
        // ------------------------------------------------------------------

        internal sealed class FakeSource : IMemorySource
        {
            public MemoryFigures Next;
            public int Reads;
            public bool Disposed;

            public void Read(ref MemoryFigures f, bool withRss)
            {
                Reads++;
                f.MonoHeap = Next.MonoHeap;
                f.Alloc = Next.Alloc;
                f.Reserved = Next.Reserved;
                f.Gfx = Next.Gfx;
                if (withRss)
                    f.Rss = Next.Rss;
            }

            public void Dispose() => Disposed = true;
        }

        [Test]
        public void Sampler_FillsThePerfSampleAndTheRun()
        {
            string statm = Path.Combine(_dir, "statm");
            File.WriteAllText(statm, "1000 409600 1 1 0 1 0\n"); // 409600 pages of 4 KB = 1600 MB (at the default 4 KB page size)
            var runs = new RunTracker();
            var source = new FakeSource { Next = new MemoryFigures { MonoHeap = 96, Alloc = 410.5f, Reserved = 512, Gfx = 120, Rss = 999 } };
            var sampler = new MemorySampler(runs, () => source, statm, 1000);
            sampler.Start(false);
            Assert.IsTrue(sampler.ReadRssNow());
            runs.Begin("run-9");
            var evt = new PerfSampleEvent { mem_mb = 80 };
            sampler.Sample(evt, evt.mem_mb);
            Assert.AreEqual(96, evt.mem_mono_heap_mb);
            Assert.AreEqual(410.5f, evt.mem_alloc_mb);
            Assert.AreEqual(512, evt.mem_reserved_mb);
            Assert.AreEqual(120, evt.mem_gfx_mb);
            float pageMb = Environment.SystemPageSize * 409600f / (1024 * 1024);
            Assert.AreEqual(pageMb, evt.mem_rss_mb, 0.01, "RSS from statm, not the engine's counter");
            Assert.AreEqual(1, source.Reads);
            var run = runs.End(RunEvent.EndEnd, null);
            Assert.AreEqual(80, run.peak_mem_mb);
            Assert.AreEqual(410.5f, run.peak_alloc_mb);
            Assert.AreEqual(pageMb, run.peak_rss_mb, 0.01);
            sampler.Dispose();
            Assert.IsTrue(source.Disposed);
        }

        [Test]
        public void Sampler_WithoutStatmTheEngineGivesRss()
        {
            var runs = new RunTracker();
            var source = new FakeSource { Next = new MemoryFigures { Alloc = 300, Rss = 777 } };
            var sampler = new MemorySampler(runs, () => source, null, 1000);
            sampler.Start(false);
            Assert.IsFalse(sampler.ReadsProcRss);
            Assert.IsFalse(sampler.ReadRssNow());
            var evt = new PerfSampleEvent();
            sampler.Sample(evt, 50);
            Assert.AreEqual(777, evt.mem_rss_mb);
            runs.Read(out var s);
            Assert.AreEqual(777, s.ProcessPeakRss);
        }

        [Test]
        public void Sampler_ThreadReadsRss()
        {
            string statm = Path.Combine(_dir, "statm");
            File.WriteAllText(statm, "1000 2560 1 1 0 1 0\n");
            var runs = new RunTracker();
            var sampler = new MemorySampler(runs, null, statm, 100);
            sampler.Start(true);
            try
            {
                var deadline = DateTime.UtcNow.AddSeconds(5);
                while (sampler.RssReadings < 2 && DateTime.UtcNow < deadline)
                    System.Threading.Thread.Sleep(20);
                Assert.GreaterOrEqual(sampler.RssReadings, 2);
                Assert.Greater(sampler.LastRssMb, 0);
                sampler.Paused = true;
                long before = sampler.RssReadings;
                System.Threading.Thread.Sleep(400);
                Assert.LessOrEqual(sampler.RssReadings, before + 1, "paused: at most the reading under way");
                sampler.Paused = false;
                deadline = DateTime.UtcNow.AddSeconds(5);
                while (sampler.RssReadings <= before + 1 && DateTime.UtcNow < deadline)
                    System.Threading.Thread.Sleep(20);
                Assert.Greater(sampler.RssReadings, before + 1, "resumed");
            }
            finally
            {
                sampler.Dispose();
            }
            Assert.IsFalse(sampler.IsRunning);
        }

        // ------------------------------------------------------------------
        // Crash journal, crash details and the run of a crashed process
        // ------------------------------------------------------------------

        private const string DeviceId = "0123456789abcdef0123456789abcdef";

        private JournalInputs Inputs(int pid, long start, RunTracker runs)
        {
            var clock = new SessionClock();
            clock.Begin(990_000, "s-9");
            return new JournalInputs
            {
                Pid = pid,
                ProcessStartMs = start,
                AppVersion = "2.2.0",
                BuildType = "release",
                Platform = "Android",
                DeviceModel = "SM-A336B",
                OsVersion = "14",
                Abi = "arm64-v8a",
                Scene = "Dungeon",
                Clock = clock,
                DeviceId = () => DeviceId,
                Timeline = new Timeline(),
                Runs = runs,
            };
        }

        private static Dictionary<string, string> ToDict(string[] pairs)
        {
            var d = new Dictionary<string, string>();
            for (int i = 0; i + 1 < pairs.Length; i += 2)
                d[pairs[i]] = pairs[i + 1];
            return d;
        }

        [Test]
        public void Journal_CarriesMemoryAndTheRun()
        {
            var runs = new RunTracker();
            runs.Record(Fig(40, 300, 900, 50), true);
            runs.Begin("run|7 \"x\"");
            runs.Record(Fig(70, 900, 2100, 80), true);
            runs.Record(Fig(60, 1200, 4300, 90), true);
            var j = new CrashJournal(_dir, Inputs(4242, 1_000_000, runs));
            string text = j.BuildText();
            StringAssert.Contains("mem_alloc_mb=1200.0\n", text);
            StringAssert.Contains("mem_rss_mb=4300.0\n", text);
            StringAssert.Contains("proc_peak_rss_mb=4300.0\n", text);
            StringAssert.Contains("run_id=run|7 \"x\"\n", text);
            StringAssert.Contains("run_peak_alloc_mb=1200.0\n", text);
            StringAssert.Contains("run_start_rss_mb=900.0\n", text);
            StringAssert.Contains("run_samples=2\n", text);

            var r = JournalRecord.Parse(Encoding.UTF8.GetBytes(text));
            Assert.IsNotNull(r);
            Assert.AreEqual("run|7 \"x\"", r.RunId);
            Assert.Greater(r.RunStartMs, 0);
            Assert.Greater(r.MemMs, 0);
            var perf = new Dictionary<string, string>();
            for (int i = 0; i < r.PerfKeys.Count; i++)
                perf[r.PerfKeys[i]] = r.PerfValues[i];
            Assert.AreEqual("4300.0", perf["run_peak_rss_mb"]);
            Assert.AreEqual("90.0", perf["mem_gfx_mb"], "the last reading");
            Assert.AreEqual("90.0", perf["run_peak_gfx_mb"]);

            // Without a run: no run lines.
            runs.End(RunEvent.EndEnd, null);
            text = j.BuildText();
            StringAssert.DoesNotContain("run_id=", text);
            StringAssert.Contains("mem_rss_mb=4300.0\n", text);
        }

        private void PreviousProcess(int pid, long start, RunTracker runs, bool foreground = true)
        {
            var inputs = Inputs(pid, start, runs);
            inputs.Paused = !foreground;
            var j = new CrashJournal(_dir, inputs);
            j.Start(false);
            j.Stop(false); // killed: nothing deleted
        }

        private sealed class Exits : IExitInfoSource
        {
            public readonly List<ExitRecord> Records = new List<ExitRecord>();
            public List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir) => Records;
        }

        [Test]
        public void Report_OomDuringARun_CrashCarriesMemoryAndTheRunIsSent()
        {
            var runs = new RunTracker();
            GameContext.Set("runId", "run-42");
            runs.Begin("run-42");
            runs.Record(Fig(70, 900, 2100), true);
            runs.RecordRss(4700);
            PreviousProcess(5150, 1_000_000, runs);
            GameContext.Set("runId", "other"); // the current process's context must not leak
            var exits = new Exits();
            long at = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() + 2000;
            exits.Records.Add(new ExitRecord { Pid = 5150, TimestampMs = at, Reason = ExitReasons.LowMemory, Importance = 100, PssKb = 4_500_000, RssKb = 4_800_000 });
            var crashes = new List<CrashEvent>();
            var runEvents = new List<RunEvent>();
            var reporter = new CrashReporter(_dir, exits, crashes.Add, () => true, ms => true) { EnqueueRun = runEvents.Add };
            reporter.ProcessPrevious(1, 1);

            Assert.AreEqual(1, crashes.Count);
            var c = crashes[0];
            Assert.AreEqual("oom", c.crash_type);
            var d = ToDict(c.details);
            Assert.AreEqual("4700.0", d["mem_rss_mb"]);
            Assert.AreEqual("900.0", d["mem_alloc_mb"]);
            Assert.AreEqual("4700.0", d["run_peak_rss_mb"]);
            Assert.AreEqual("4700.0", d["proc_peak_rss_mb"]);
            Assert.AreEqual("run-42", d["run_id"]);
            Assert.GreaterOrEqual(long.Parse(d["mem_age_ms"], CultureInfo.InvariantCulture), 1900);
            Assert.GreaterOrEqual(long.Parse(d["run_ms"], CultureInfo.InvariantCulture), 1900);

            Assert.AreEqual(1, runEvents.Count);
            var run = runEvents[0];
            Assert.AreEqual("run-42", run.run_id);
            Assert.AreEqual("crash", run.end_reason);
            Assert.AreEqual("oom", run.crash_type);
            Assert.AreEqual(at, run.timestamp);
            Assert.AreEqual(4700, run.peak_rss_mb);
            Assert.AreEqual(900, run.peak_alloc_mb);
            Assert.AreEqual(4700, run.end_rss_mb);
            Assert.AreEqual(1, run.samples);
            Assert.AreEqual("s-9", run.session_id);
            Assert.AreEqual(DeviceId, run.device_id);
            Assert.AreEqual("2.2.0", run.app_version);
            Assert.IsTrue(run.ctx.TryGetValue("runId", out var ctxRun) && ctxRun == "run-42");
            Assert.AreEqual(32, run.event_id.Length);
            Assert.AreNotEqual(c.event_id, run.event_id);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length, "journal deleted once reported");

            string json = JsonSerializer.SerializeEvent(run);
            StringAssert.Contains("\"type\":\"run\"", json);
            StringAssert.Contains("\"run_id\":\"run-42\",\"end_reason\":\"crash\",\"crash_type\":\"oom\"", json);
            StringAssert.Contains("\"peak_rss_mb\":4700", json);
            StringAssert.Contains("\"ctx\":{\"runId\":\"run-42\"}", json);
        }

        [Test]
        public void RunEventId_LiveAndJournalCopiesAreTheSame()
        {
            var runs = new RunTracker();
            runs.Begin("run-77");
            var j = new CrashJournal(_dir, Inputs(5151, 1_000_000, runs));
            var record = JournalRecord.Parse(Encoding.UTF8.GetBytes(j.BuildText()));
            var fromJournal = CrashReporter.BuildRun(record, null, null);
            var live = runs.End(RunEvent.EndEnd, null);
            string liveId = CrashReporter.EventId(DeviceId, 5151, 1_000_000, CrashReporter.RunKind(live.run_id, live.run_start_ms));
            Assert.AreEqual(fromJournal.event_id, liveId, "the server keeps one of them");
            runs.Begin("run-78");
            var other = runs.End(RunEvent.EndEnd, null);
            Assert.AreNotEqual(liveId, CrashReporter.EventId(DeviceId, 5151, 1_000_000, CrashReporter.RunKind(other.run_id, other.run_start_ms)));
        }

        [Test]
        public void Sampler_FailedRssReadingIsNotKept()
        {
            string statm = Path.Combine(_dir, "statm");
            File.WriteAllText(statm, "1000 2560 1 1 0 1 0\n");
            var runs = new RunTracker();
            var sampler = new MemorySampler(runs, null, statm, 1000);
            sampler.Start(false);
            Assert.IsTrue(sampler.ReadRssNow());
            Assert.Greater(sampler.LastRssMb, 0);
            File.WriteAllText(statm, "garbage\n");
            Assert.IsFalse(sampler.ReadRssNow());
            Assert.AreEqual(0, sampler.LastRssMb);
            runs.Read(out var s);
            Assert.AreEqual(0, s.Last.Rss);
            Assert.Greater(s.ProcessPeakRss, 0, "the peak is kept");
            var evt = new PerfSampleEvent();
            sampler.Sample(evt, 10);
            Assert.AreEqual(0, evt.mem_rss_mb, "not sent");
            sampler.Dispose();
        }

        [Test]
        public void Report_BackgroundKillDuringARun_SendsTheRunOnly()
        {
            var runs = new RunTracker();
            runs.Begin("run-5");
            runs.Record(Fig(50, 500, 1000), true);
            PreviousProcess(6000, 2_000_000, runs, foreground: false);
            var exits = new Exits();
            exits.Records.Add(new ExitRecord { Pid = 6000, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.LowMemory, Importance = 400 });
            var crashes = new List<CrashEvent>();
            var runEvents = new List<RunEvent>();
            var reporter = new CrashReporter(_dir, exits, crashes.Add, () => true, ms => true) { EnqueueRun = runEvents.Add };
            reporter.ProcessPrevious(1, 1);
            Assert.AreEqual(0, crashes.Count);
            Assert.AreEqual(1, reporter.NotCrashes);
            Assert.AreEqual(1, runEvents.Count);
            Assert.AreEqual("process_end", runEvents[0].end_reason);
            Assert.IsNull(runEvents[0].crash_type);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length);

            // A background kill without a run: nothing sent, the journal deleted.
            PreviousProcess(6001, 3_000_000, new RunTracker(), foreground: false);
            exits.Records.Add(new ExitRecord { Pid = 6001, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.LowMemory, Importance = 400 });
            runEvents.Clear();
            reporter.ProcessPrevious(1, 1);
            Assert.AreEqual(0, runEvents.Count);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length);
        }

        [Test]
        public void Report_NoConsent_NoRun()
        {
            var runs = new RunTracker();
            runs.Begin("run-6");
            PreviousProcess(7000, 4_000_000, runs);
            var runEvents = new List<RunEvent>();
            var reporter = new CrashReporter(_dir, new Exits(), new List<CrashEvent>().Add, () => false, ms => true) { EnqueueRun = runEvents.Add };
            reporter.ProcessPrevious(1, 1);
            Assert.AreEqual(0, runEvents.Count);
            Assert.Greater(Directory.GetFiles(_dir).Length, 0, "kept until consent is granted");
        }

        // ------------------------------------------------------------------
        // Wire format
        // ------------------------------------------------------------------

        [Test]
        public void Json_PerfSampleBreakdownAndRun()
        {
            var perf = new PerfSampleEvent { fps = 60, mem_mb = 80.5f, interval_ms = 1003, mem_mono_heap_mb = 96, mem_alloc_mb = 410.25f, mem_reserved_mb = 512, mem_gfx_mb = 0, mem_rss_mb = 1500.5f };
            string json = JsonSerializer.SerializeEvent(perf);
            StringAssert.Contains("\"mem_mb\":80.50,\"interval_ms\":1003,\"mem_mono_heap_mb\":96.00,\"mem_alloc_mb\":410.25,\"mem_reserved_mb\":512.00,\"mem_rss_mb\":1500.50", json);
            StringAssert.DoesNotContain("mem_gfx_mb", json, "0: not available, not sent");

            var old = JsonSerializer.SerializeEvent(new PerfSampleEvent { fps = 30, mem_mb = 70 });
            StringAssert.DoesNotContain("interval_ms", old);
            StringAssert.DoesNotContain("mem_rss_mb", old);

            var run = new RunEvent { run_id = "r\"1", end_reason = "end", result = "win", duration_ms = 600000, samples = 600, peak_rss_mb = 1900, start_rss_mb = 1400, end_rss_mb = 1850 };
            json = JsonSerializer.SerializeEvent(run);
            StringAssert.Contains("\"type\":\"run\"", json);
            StringAssert.Contains("\"run_id\":\"r\\\"1\",\"end_reason\":\"end\",\"result\":\"win\",\"duration_ms\":600000,\"samples\":600,\"peak_rss_mb\":1900.00,\"start_rss_mb\":1400.00,\"end_rss_mb\":1850.00", json);
            StringAssert.DoesNotContain("crash_type", json);
            StringAssert.DoesNotContain("peak_alloc_mb", json);
        }
    }
}
