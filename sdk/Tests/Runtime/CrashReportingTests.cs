using System;
using System.Collections.Generic;
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
    /// APM-7 through ApmClient: a process killed with a crash journal is
    /// reported at the next launch with its session, context and breadcrumbs;
    /// a clean exit is not; consent (APM-14) governs the journal.
    /// </summary>
    [TestFixture]
    public class CrashReportingTests
    {
        private static readonly string[] Keys = { "ozx_apm_device_id", "ozx_apm_session_id", "ozx_apm_session_start",
            ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey };

        private string _dir;
        private string _crashDir;
        private FakeExits _exits;

        private sealed class FakeExits : IExitInfoSource
        {
            public readonly List<ExitRecord> Records = new List<ExitRecord>();
            public readonly Dictionary<int, byte[]> Traces = new Dictionary<int, byte[]>();

            public List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir)
            {
                foreach (var r in Records)
                {
                    if (Traces.TryGetValue(r.Pid, out var data) && wantTrace.Contains(r.Pid))
                    {
                        r.TracePath = Path.Combine(traceDir, CrashReporter.TracePrefix + r.Pid + ".bin");
                        File.WriteAllBytes(r.TracePath, data);
                    }
                }
                return Records;
            }
        }

        private void ResetAll()
        {
            ApmClient.ResetForTests();
            foreach (var key in Keys)
                PlayerPrefs.DeleteKey(key);
            DeviceInfo.ClearCacheForTests();
            GameContext.ResetForTests();
        }

        [SetUp]
        public void SetUp()
        {
            ResetAll();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_crash_client_" + Guid.NewGuid().ToString("N"));
            _crashDir = Path.Combine(_dir, "crash");
            _exits = new FakeExits();
        }

        [TearDown]
        public void TearDown()
        {
            ResetAll();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private ApmClient Launch(int pid, ConsentState? consent = null)
        {
            ApmClient.StoragePathForTests = Path.Combine(_dir, "events");
            ApmClient.CrashDirForTests = _crashDir;
            ApmClient.ExitInfoForTests = _exits;
            ApmClient.PidForTests = pid;
            var config = new ApmConfig
            {
                ServerUrl = "http://127.0.0.1:9",
                AppVersion = "2.1.0",
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f,
            };
            if (consent.HasValue)
                config.DefaultTechnicalConsent = consent.Value;
            ApmClient.Initialize(config);
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        /// <summary>The process dies: no shutdown, the journal stays.</summary>
        private static void Kill(ApmClient client)
        {
            client.CrashMonitorForTests.StopJournal(false);
            client.StorageForTests.SimulateCrashForTests();
            ApmClient.ResetForTests();
            DeviceInfo.ClearCacheForTests();
        }

        private string StoredText()
        {
            string events = Path.Combine(_dir, "events");
            return Directory.Exists(events)
                ? string.Concat(Directory.GetFiles(events).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        // Processes with a journal: each writes two files in turn
        // (j-<pid>-<start>-a/b.txt), so files are not processes.
        private int JournalFiles => !Directory.Exists(_crashDir) ? 0 :
            Directory.GetFiles(_crashDir, CrashJournal.FilePrefix + "*")
                .Select(f => { var n = Path.GetFileName(f); return n.Substring(0, n.LastIndexOf('-')); })
                .Distinct().Count();

        [Test]
        public void KilledProcess_IsReportedAtTheNextLaunch_WithItsSessionAndContext()
        {
            var first = Launch(1001);
            string session = first.Session.SessionId;
            ApmClient.SetContext("roomId", "r-12");
            ApmClient.TrackEvent("room_enter", "roomId", "r-12");
            first.CrashMonitorForTests.Journal.WriteNow();
            Assert.AreEqual(1, JournalFiles);
            Kill(first);

            _exits.Records.Add(new ExitRecord { Pid = 1001, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.LowMemory, Importance = 100, PssKb = 900000 });
            var second = Launch(1002);
            ApmClient.SetContext("roomId", "r-99");
            Assert.AreNotEqual(session, second.Session.SessionId);
            var reporter = second.CrashMonitorForTests.Reporter;
            Assert.AreEqual(1, reporter.Reported);
            var e = reporter.LastEvents.Single();
            Assert.AreEqual("oom", e.crash_type);
            Assert.AreEqual(session, e.session_id);
            Assert.IsTrue(e.ctx.TryGetValue("roomId", out var room) && room == "r-12");
            StringAssert.EndsWith("Event: room_enter roomId=r-12", e.breadcrumbs.Last());

            second.StorageForTests.Pump();
            string stored = StoredText();
            StringAssert.Contains("\"type\":\"crash\"", stored);
            StringAssert.Contains("\"session_id\":\"" + session + "\"", stored);
            StringAssert.Contains("\"crash_type\":\"oom\"", stored);
            StringAssert.Contains("\"exit_reason\":\"LOW_MEMORY\"", stored);
            Assert.AreEqual(1, JournalFiles, "the old journal is gone, the new process has its own");
            Assert.IsTrue(ApmClient.GetStats().CrashReporting);
            Assert.AreEqual(1, ApmClient.GetStats().CrashesReported);
        }

        [Test]
        public void CleanShutdown_IsNotACrash()
        {
            var first = Launch(1001);
            first.CrashMonitorForTests.Journal.WriteNow();
            ApmClient.Shutdown();
            // Kept, marked clean, until the next launch has seen the exit record.
            Assert.AreEqual(1, JournalFiles);
            StringAssert.Contains("\nclean=1\n", string.Concat(Directory.GetFiles(_crashDir).Select(File.ReadAllText)));
            ApmClient.ResetForTests();
            // Application.Quit on the A33: SIGNALED 9, importance 400.
            _exits.Records.Add(new ExitRecord { Pid = 1001, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.Signaled, Status = 9, Importance = 400 });
            var second = Launch(1002);
            Assert.AreEqual(0, second.CrashMonitorForTests.Reporter.Reported);
            Assert.AreEqual(1, second.CrashMonitorForTests.Reporter.NotCrashes);
            Assert.AreEqual(1, JournalFiles, "the old journal is gone, the new process has its own");
        }

        [Test]
        public void CleanQuitAfterAnAnrTheProcessSurvived_ReportsTheAnrWithItsSession()
        {
            var first = Launch(4796);
            string session = first.Session.SessionId;
            ApmClient.SetContext("roomId", "r-anr");
            first.CrashMonitorForTests.Journal.WriteNow();
            ApmClient.Shutdown();
            ApmClient.ResetForTests();

            _exits.Records.Add(new ExitRecord { Pid = 4796, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.Signaled, Status = 9, Importance = 400 });
            _exits.Traces[4796] = CrashTraceParserTests.AnrTraceOfASurvivedAnr();
            var second = Launch(4797);
            var reporter = second.CrashMonitorForTests.Reporter;
            Assert.AreEqual(1, reporter.Reported);
            var e = reporter.LastEvents.Single();
            Assert.AreEqual("anr", e.crash_type);
            Assert.AreEqual(session, e.session_id);
            Assert.IsTrue(e.ctx.TryGetValue("roomId", out var room) && room == "r-anr");
            second.StorageForTests.Pump();
            StringAssert.Contains("\"crash_type\":\"anr\"", StoredText());
            Assert.AreEqual(1, JournalFiles);
        }

        [Test]
        public void PausedThenKilled_IsNotACrash()
        {
            var first = Launch(1001);
            first.PauseForTests(true);
            first.CrashMonitorForTests.Journal.WriteNow();
            Kill(first);
            _exits.Records.Add(new ExitRecord { Pid = 1001, TimestampMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), Reason = ExitReasons.LowMemory, Importance = 400 });
            var second = Launch(1002);
            Assert.AreEqual(0, second.CrashMonitorForTests.Reporter.Reported);
            Assert.AreEqual(1, second.CrashMonitorForTests.Reporter.NotCrashes);
        }

        [Test]
        public void Denied_DeletesEveryJournal_AndUndecidedDeletesOnlyTheOwn()
        {
            var first = Launch(1001);
            Kill(first);
            var second = Launch(1002);
            // The first process's journal was reported ("unknown", no exit record).
            Assert.AreEqual(1, second.CrashMonitorForTests.Reporter.Reported);
            Kill(second);

            var third = Launch(1003, ConsentState.Undecided);
            Assert.IsNull(third.CrashMonitorForTests.Journal, "no journal without consent");
            Assert.AreEqual(1, JournalFiles, "the second process's journal is kept while undecided");
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            Assert.IsNotNull(third.CrashMonitorForTests.Journal);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Undecided);
            Assert.IsNull(third.CrashMonitorForTests.Journal);

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Assert.IsFalse(Directory.Exists(_crashDir), "denied: the directory is gone");
        }

        [Test]
        public void DeniedAtLaunch_DeletesJournalsLeftBehind()
        {
            var first = Launch(1001);
            Kill(first);
            Assert.AreEqual(1, JournalFiles);
            Launch(1002, ConsentState.Denied);
            Assert.IsFalse(Directory.Exists(_crashDir));
        }

        [Test]
        public void ResetDeviceId_DeletesJournalsOfEarlierProcesses()
        {
            var first = Launch(1001, ConsentState.Undecided);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            first.CrashMonitorForTests.Journal.WriteNow();
            Kill(first);
            // Undecided at the next launch: the old journal waits.
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Undecided);
            var second = Launch(1002);
            Assert.AreEqual(1, JournalFiles);
            ApmClient.ResetDeviceId();
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            Assert.AreEqual(0, second.CrashMonitorForTests.Reporter?.Reported ?? 0, "the old process (old device id) is not reported");
        }

        [Test]
        public void Update_DoesNotAllocate_WithCrashReporting()
        {
            var client = Launch(1001);
            for (int i = 0; i < 5; i++)
                client.UpdateForTests();
            Assert.That(() => { client.UpdateForTests(); }, Is.Not.AllocatingGCMemory());
        }
    }
}
