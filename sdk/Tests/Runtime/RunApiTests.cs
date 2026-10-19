using System;
using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-9 through ApmClient (EditMode: needs the Unity runtime):
    /// BeginRun / EndRun with the run event and its peaks, the runId
    /// context key, a run open at shutdown, SetContextCount, consent, and no
    /// allocation on the sampling paths.
    /// </summary>
    [TestFixture]
    public class RunApiTests
    {
        private string _dir;
        private string _statm;
        private MemoryRunTests.FakeSource _source;

        private static void ResetPrefs()
        {
            foreach (var key in new[] { "ozx_apm_device_id", "ozx_apm_session_id", "ozx_apm_session_start", ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey })
                PlayerPrefs.DeleteKey(key);
            DeviceInfo.ClearCacheForTests();
        }

        [SetUp]
        public void SetUp()
        {
            ApmClient.ResetForTests();
            ResetPrefs();
            GameContext.ResetForTests();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_runs_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_dir);
            _statm = Path.Combine(_dir, "statm");
            File.WriteAllText(_statm, "1000 256000 1 1 0 1 0\n");
            _source = new MemoryRunTests.FakeSource { Next = new MemoryFigures { MonoHeap = 96, Alloc = 400, Reserved = 512, Gfx = 120 } };
        }

        [TearDown]
        public void TearDown()
        {
            ApmClient.ResetForTests();
            ResetPrefs();
            GameContext.ResetForTests();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private ApmClient Start(Action<ApmConfig> configure = null)
        {
            var config = new ApmConfig
            {
                ServerUrl = "http://127.0.0.1:9",
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f,
            };
            configure?.Invoke(config);
            ApmClient.StoragePathForTests = Path.Combine(_dir, "events");
            ApmClient.ProcStatmPathForTests = _statm;
            ApmClient.MemorySourceForTests = _source;
            ApmClient.Initialize(config);
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        private string Stored(ApmClient client)
        {
            ApmClient.Flush();
            client.StorageForTests.Pump();
            string events = Path.Combine(_dir, "events");
            return Directory.Exists(events)
                ? string.Concat(Directory.GetFiles(events).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        private static string[] Lines(string stored, string type) =>
            stored.Split('\n').Where(l => l.StartsWith("{\"type\":\"" + type + "\"")).ToArray();

        [Test]
        public void BeforeInitialize_NothingIsTracked()
        {
            Assert.IsFalse(ApmClient.BeginRun("run-1"));
            Assert.IsFalse(ApmClient.EndRun());
            Assert.IsNull(ApmClient.CurrentRunId);
            Assert.IsTrue(ApmClient.SetContextCount("aliveEnemies", 7));
            Assert.AreEqual("6-10", ApmClient.GetContext("aliveEnemies"));
        }

        [Test]
        public void BeginEndRun_SendsTheRunWithItsPeaks()
        {
            var client = Start();
            var sampler = client.MemorySamplerForTests;
            Assert.IsTrue(sampler.ReadRssNow());
            Assert.IsTrue(ApmClient.BeginRun("run-1"));
            Assert.AreEqual("run-1", ApmClient.CurrentRunId);
            Assert.AreEqual("run-1", ApmClient.GetContext("runId"));
            _source.Next.Alloc = 900;
            client.SampleMemory(new PerfSampleEvent { mem_mb = 70 });
            _source.Next.Alloc = 600;
            client.SampleMemory(new PerfSampleEvent { mem_mb = 50 });
            Assert.IsTrue(ApmClient.EndRun("win"));
            Assert.IsNull(ApmClient.CurrentRunId);
            Assert.IsNull(ApmClient.GetContext("runId"));
            Assert.IsFalse(ApmClient.EndRun(), "no run open");

            var runs = Lines(Stored(client), "run");
            Assert.AreEqual(1, runs.Length);
            StringAssert.Contains("\"run_id\":\"run-1\",\"end_reason\":\"end\",\"result\":\"win\"", runs[0]);
            StringAssert.Contains("\"samples\":2", runs[0]);
            StringAssert.Contains("\"peak_mem_mb\":70.00", runs[0]);
            StringAssert.Contains("\"peak_alloc_mb\":900.00", runs[0]);
            StringAssert.Contains("\"end_alloc_mb\":600.00", runs[0]);
            StringAssert.Contains("\"peak_rss_mb\":", runs[0]);
            StringAssert.Contains("\"ctx\":{\"runId\":\"run-1\"}", runs[0], "the run's context");
            Assert.AreEqual(1, ApmClient.GetStats().RunsEnded);
        }

        [Test]
        public void BeginRun_EndsTheOpenRun_InvalidIdsAndResults()
        {
            var client = Start();
            Assert.IsFalse(ApmClient.BeginRun(""));
            Assert.IsFalse(ApmClient.BeginRun("a\nb"));
            Assert.IsTrue(ApmClient.BeginRun("r1"));
            Assert.IsTrue(ApmClient.BeginRun("r2"));
            Assert.IsTrue(ApmClient.EndRun("not valid!"));
            var runs = Lines(Stored(client), "run");
            Assert.AreEqual(2, runs.Length);
            StringAssert.Contains("\"run_id\":\"r1\",\"end_reason\":\"new_run\"", runs[0]);
            StringAssert.Contains("\"ctx\":{\"runId\":\"r1\"}", runs[0]);
            StringAssert.Contains("\"run_id\":\"r2\",\"end_reason\":\"end\",\"duration_ms\"", runs[1], "the invalid result is left out");
        }

        [Test]
        public void EndRun_KeepsAContextTheGameChanged()
        {
            Start();
            ApmClient.BeginRun("r1");
            ApmClient.SetContext("runId", "the-games-own");
            ApmClient.EndRun();
            Assert.AreEqual("the-games-own", ApmClient.GetContext("runId"));
        }

        [Test]
        public void Shutdown_EndsTheOpenRun()
        {
            Start();
            ApmClient.BeginRun("r-quit");
            ApmClient.Shutdown(); // without a worker thread the storage writes on this thread
            string events = Path.Combine(_dir, "events");
            string stored = string.Concat(Directory.GetFiles(events).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText));
            StringAssert.Contains("\"run_id\":\"r-quit\",\"end_reason\":\"shutdown\"", stored);
        }

        [Test]
        public void NoConsent_RunIsNotSent()
        {
            var client = Start(c => c.DefaultTechnicalConsent = ConsentState.Undecided);
            Assert.IsTrue(ApmClient.BeginRun("r1"));
            Assert.IsTrue(ApmClient.EndRun());
            Assert.AreEqual(0, Lines(Stored(client), "run").Length);
            Assert.IsFalse(client.MemorySamplerForTests.IsRunning, "no RSS thread without consent");
        }

        [Test]
        public void SetContextCount_ChangesTheContextOnlyBetweenRanges()
        {
            Start();
            Assert.IsTrue(ApmClient.SetContextCount("aliveEnemies", 3));
            var before = GameContext.Current;
            ApmClient.SetContextCount("aliveEnemies", 4);
            ApmClient.SetContextCount("aliveEnemies", 5);
            Assert.AreSame(before, GameContext.Current, "same range: no new snapshot");
            ApmClient.SetContextCount("aliveEnemies", 12);
            Assert.AreEqual("11-20", ApmClient.GetContext("aliveEnemies"));
            Assert.IsFalse(ApmClient.SetContextCount("bad key", 1));
            // Statement lambdas: an expression lambda returning a value is an
            // ActualValueDelegate, which the constraint refuses.
            Assert.That(() => { ApmClient.SetContextCount("aliveEnemies", 15); }, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void SamplingPaths_DoNotAllocate()
        {
            var client = Start();
            ApmClient.BeginRun("r1");
            var evt = new PerfSampleEvent { mem_mb = 50 };
            var sampler = client.MemorySamplerForTests;
            client.SampleMemory(evt);
            sampler.ReadRssNow();
            Assert.That(() => { client.SampleMemory(evt); }, Is.Not.AllocatingGCMemory());
            Assert.That(() => { sampler.ReadRssNow(); }, Is.Not.AllocatingGCMemory());
            var stats = ApmClient.GetStats();
            Assert.IsTrue(stats.MemoryBreakdown);
            Assert.IsTrue(stats.ProcRss);
            Assert.AreEqual("r1", stats.RunId);
            Assert.Greater(stats.RssMb, 0);
        }

        [Test]
        public void JournalWriteWithMemoryAndRun_DoesNotAllocateOnceWarm()
        {
            var runs = new RunTracker();
            runs.Record(new MemoryFigures { Managed = 50, MonoHeap = 60, Alloc = 400, Reserved = 500, Gfx = 80, Rss = 1200 }, true);
            runs.Begin("run-77");
            runs.RecordRss(1500);
            var clock = new SessionClock();
            clock.Begin(990_000, "s-1");
            var j = new CrashJournal(Path.Combine(_dir, "crash"), new JournalInputs
            {
                Pid = 4100, ProcessStartMs = 1000, AppVersion = "1", Clock = clock, DeviceId = () => "d", Timeline = new Timeline(), Runs = runs,
            });
            j.Start(false);
            try
            {
                j.WriteNow();
                StringAssert.Contains("run_peak_rss_mb=1500.0", j.BuildText());
                Assert.That(() => { j.WriteNow(); }, Is.Not.AllocatingGCMemory());
            }
            finally
            {
                j.Stop(true);
            }
        }
    }
}
