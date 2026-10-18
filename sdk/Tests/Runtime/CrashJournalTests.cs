using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using System.Threading;
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
    /// APM-7: the crash journal (written, read back, torn writes, escaping)
    /// and the report of earlier processes (classification, event fields,
    /// journals deleted once the events are on disk).
    /// </summary>
    [TestFixture]
    public class CrashJournalTests
    {
        private string _dir;
        private Timeline _timeline;
        private SessionClock _clock;
        private const string SessionId = "1760000000000_ab12cd34";
        private const string DeviceId = "0123456789abcdef0123456789abcdef";

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_crash_test_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_dir);
            _timeline = new Timeline();
            _clock = new SessionClock();
            _clock.Begin(990_000, SessionId);
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

        private JournalInputs Inputs(int pid, long start) => new JournalInputs
        {
            Pid = pid,
            ProcessStartMs = start,
            AppVersion = "2.1.0",
            BuildType = "release",
            Platform = "Android",
            DeviceModel = "SM-A336B",
            OsVersion = "Android OS 14 / API-34",
            Gpu = "Mali-G68",
            RamClass = "6GB",
            Abi = "arm64-v8a",
            NetType = "wifi",
            Scene = "Dungeon",
            Clock = _clock,
            DeviceId = () => DeviceId,
            Timeline = _timeline,
        };

        private CrashJournal NewJournal(int pid, long start, Action<JournalInputs> edit = null)
        {
            var inputs = Inputs(pid, start);
            edit?.Invoke(inputs);
            return new CrashJournal(_dir, inputs);
        }

        [Test]
        public void Journal_RoundTripsContextBreadcrumbsAndState()
        {
            GameContext.Set("roomId", "r-12");
            GameContext.Set("note", "line1\nline2 \\ back");
            _timeline.RecordText("Scene: Dungeon");
            _timeline.RecordEvent("room_enter", new[] { "roomId", "r-12" }, Timeline.NowMs());
            _timeline.OnLog("Asset not found: anim/x\nsecond line", "", LogType.Warning, true);

            var j = NewJournal(4000, 1000);
            j.Start(false);
            var recs = CrashJournal.ReadPrevious(_dir, 1, 1, null);
            Assert.AreEqual(1, recs.Count);
            var r = recs[0];
            Assert.AreEqual(4000, r.Pid);
            Assert.AreEqual(1000, r.StartMs);
            Assert.AreEqual(SessionId, r.SessionId);
            Assert.AreEqual(DeviceId, r.DeviceId);
            Assert.AreEqual("2.1.0", r.AppVersion);
            Assert.AreEqual("Dungeon", r.Scene);
            Assert.AreEqual("arm64-v8a", r.Abi);
            Assert.IsTrue(r.Foreground);
            CollectionAssert.AreEqual(new[] { "note", "roomId" }, r.ContextKeys);
            CollectionAssert.AreEqual(new[] { "line1\nline2 \\ back", "r-12" }, r.ContextValues);
            Assert.AreEqual(3, r.Breadcrumbs.Count);
            StringAssert.EndsWith("Scene: Dungeon", r.Breadcrumbs[0]);
            StringAssert.EndsWith("Event: room_enter roomId=r-12", r.Breadcrumbs[1]);
            StringAssert.EndsWith("Warning: Asset not found: anim/x\nsecond line", r.Breadcrumbs[2]);
            j.Stop(true);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length, "a clean stop deletes the journal");
        }

        [Test]
        public void Journal_AlternatesSlotsAndSurvivesATornWrite()
        {
            var j = NewJournal(4000, 1000);
            j.Start(false);
            GameContext.Set("roomId", "r-1");
            j.WriteNow();
            GameContext.Set("roomId", "r-2");
            j.WriteNow(); // seq 3, slot a
            j.Stop(false);
            Assert.AreEqual(2, Directory.GetFiles(_dir).Length);
            var r = CrashJournal.ReadPrevious(_dir, 1, 1, null).Single();
            Assert.AreEqual(3, r.Seq);
            Assert.AreEqual("r-2", r.ContextValues[0]);
            Assert.AreEqual(2, r.Files.Count);

            // The process died in the middle of the newest write: its file is
            // cut; the other slot (the write before) is used.
            string newest = j.Paths[0];
            var bytes = File.ReadAllBytes(newest);
            File.WriteAllBytes(newest, bytes.Take(bytes.Length / 2).ToArray());
            var invalid = new List<string>();
            r = CrashJournal.ReadPrevious(_dir, 1, 1, invalid).Single();
            Assert.AreEqual(2, r.Seq);
            Assert.AreEqual("r-1", r.ContextValues[0]);
            Assert.AreEqual(0, invalid.Count, "the torn slot is listed with its process, to be deleted with it");
            Assert.AreEqual(2, r.Files.Count);

            // A flipped byte fails the checksum.
            var other = File.ReadAllBytes(j.Paths[1]);
            other[20] ^= 1;
            File.WriteAllBytes(j.Paths[1], other);
            invalid.Clear();
            Assert.AreEqual(0, CrashJournal.ReadPrevious(_dir, 1, 1, invalid).Count);
            Assert.AreEqual(2, invalid.Count);
        }

        [Test]
        public void ReadPrevious_SkipsTheOwnProcessAndOrdersNewestFirst()
        {
            NewJournal(100, 1000).Start(false);
            NewJournal(200, 3000).Start(false);
            NewJournal(300, 2000).Start(false);
            var recs = CrashJournal.ReadPrevious(_dir, 200, 3000, null);
            CollectionAssert.AreEqual(new[] { 300, 100 }, recs.Select(x => x.Pid).ToArray());
        }

        [Test]
        public void JournalThread_WritesChangesWithinTheMinimumInterval()
        {
            var j = NewJournal(4000, 1000);
            j.Start(true);
            try
            {
                Assert.IsTrue(WaitFor(() => Interlocked.Read(ref j.Writes) >= 1, 2000), "first write");
                long before = Interlocked.Read(ref j.Writes);
                _timeline.RecordText("Loading room r-13");
                Assert.IsTrue(WaitFor(() => Interlocked.Read(ref j.Writes) > before, 1000), "a change is written");
                var r = CrashJournal.ReadPrevious(_dir, 1, 1, null).Single();
                StringAssert.EndsWith("Loading room r-13", r.Breadcrumbs.Last());

                // A burst of 200 changes is coalesced into a few writes.
                before = Interlocked.Read(ref j.Writes);
                for (int i = 0; i < 200; i++)
                    GameContext.Set("tick", i.ToString());
                Thread.Sleep(3 * CrashJournal.MinIntervalMs);
                long writes = Interlocked.Read(ref j.Writes) - before;
                Assert.That(writes, NUnit.Framework.Is.InRange(1, 5));
                Assert.IsTrue(WaitFor(() => CrashJournal.ReadPrevious(_dir, 1, 1, null).Single().ContextValues.Contains("199"), 1000));
            }
            finally
            {
                j.Stop(true);
            }
            Assert.IsFalse(j.IsRunning);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length);
        }

        [Test]
        public void NotifyChanged_DoesNotAllocate()
        {
            var j = NewJournal(4000, 1000);
            j.Start(true);
            try
            {
                CrashJournal.NotifyChanged();
                Assert.That(() => { CrashJournal.NotifyChanged(); }, Is.Not.AllocatingGCMemory());
                _timeline.OnLog("warming up", "", LogType.Warning, true);
                // A statement lambda: OnLog returns a value, and an expression lambda
                // would be an ActualValueDelegate, which the constraint refuses.
                Assert.That(() => { _timeline.OnLog("Pool empty for enemy 12", "", LogType.Warning, true); }, Is.Not.AllocatingGCMemory());
            }
            finally
            {
                j.Stop(true);
            }
        }

        [Test]
        public void JournalWrite_DoesNotAllocateOnceWarm()
        {
            GameContext.Set("roomId", "r-12");
            _timeline.RecordEvent("room_enter", new[] { "roomId", "r-12" }, Timeline.NowMs());
            _timeline.OnLog("Asset not found: anim/x", "", LogType.Warning, true);
            var j = NewJournal(4000, 1000);
            j.Start(false);
            j.WriteNow();
            j.WriteNow();
            Assert.That(() => { j.WriteNow(); }, Is.Not.AllocatingGCMemory());
            j.Stop(true);
        }

        private static bool WaitFor(Func<bool> cond, int ms)
        {
            var until = DateTime.UtcNow.AddMilliseconds(ms);
            while (DateTime.UtcNow < until)
            {
                if (cond())
                    return true;
                Thread.Sleep(10);
            }
            return cond();
        }

        // ------------------------------------------------------------------
        // CrashReporter
        // ------------------------------------------------------------------

        private sealed class FakeExits : IExitInfoSource
        {
            public List<ExitRecord> Records = new List<ExitRecord>();
            public Dictionary<int, byte[]> Traces = new Dictionary<int, byte[]>();
            public ICollection<int> AskedFor;

            public List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir)
            {
                AskedFor = wantTrace;
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

        private CrashReporter Reporter(FakeExits exits, List<CrashEvent> sink, bool allowed = true, bool persisted = true) =>
            new CrashReporter(_dir, exits, sink.Add, () => allowed, ms => persisted);

        private void PreviousProcess(int pid, long start, bool foreground, Action before = null)
        {
            before?.Invoke();
            var j = NewJournal(pid, start, i => i.Paused = !foreground);
            j.Start(false);
            j.Stop(false); // killed: nothing deleted
        }

        [Test]
        public void Report_NativeCrashWithTombstone()
        {
            GameContext.Set("roomId", "r-12");
            PreviousProcess(12345, 1_000_000, true, () => _timeline.RecordEvent("room_enter", new[] { "roomId", "r-12" }, Timeline.NowMs()));
            GameContext.Set("roomId", "r-99"); // the current process's context must not leak into the report
            var exits = new FakeExits();
            exits.Records.Add(new ExitRecord { Pid = 12345, TimestampMs = 1_034_000, Reason = ExitReasons.CrashNative, Status = 11, Importance = 100, PssKb = 512000, RssKb = 600000, Description = "crash" });
            exits.Records.Add(new ExitRecord { Pid = 999, TimestampMs = 900_000, Reason = ExitReasons.LowMemory }); // another, unrelated process
            exits.Traces[12345] = CrashTraceParserTests.NativeTombstone();
            var sink = new List<CrashEvent>();
            var reporter = Reporter(exits, sink);
            reporter.ProcessPrevious(1, 1);

            Assert.AreEqual(1, sink.Count);
            var e = sink[0];
            Assert.AreEqual("native", e.crash_type);
            Assert.AreEqual("CRASH_NATIVE", e.exit_reason);
            Assert.AreEqual(11, e.exit_status);
            Assert.AreEqual(1_034_000, e.timestamp);
            Assert.AreEqual("SIGSEGV", e.signal);
            Assert.AreEqual("SEGV_MAPERR", e.signal_code);
            StringAssert.StartsWith("Thread \"UnityMain\" tid=12380 crashed\n#00 pc 00000000005417bc libil2cpp.so (BuildId: ", e.stack);
            Assert.AreEqual(SessionId, e.session_id);
            Assert.AreEqual(DeviceId, e.device_id);
            Assert.AreEqual("2.1.0", e.app_version);
            Assert.AreEqual("Dungeon", e.scene);
            Assert.AreEqual("release", e.build_type);
            Assert.AreEqual("arm64-v8a", e.abi);
            Assert.IsTrue(e.foreground);
            Assert.AreEqual(512000, e.pss_kb);
            Assert.IsTrue(e.ctx.TryGetValue("roomId", out var room) && room == "r-12");
            Assert.AreEqual(1, e.breadcrumbs.Count);
            StringAssert.EndsWith("Event: room_enter roomId=r-12", e.breadcrumbs[0]);
            Assert.AreEqual(32, e.event_id.Length);
            Assert.IsNotEmpty(e.fingerprint);
            var details = ToDict(e.details);
            Assert.AreEqual("exit_info", details["detected_by"]);
            Assert.AreEqual("proto", details["trace"]);
            Assert.AreEqual("null pointer dereference", details["cause"]);
            Assert.AreEqual("34000", details["uptime_ms"]);
            // Journals and the copied trace are gone; the trace was asked for this pid only.
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length);
            CollectionAssert.AreEqual(new[] { 12345 }, exits.AskedFor.ToArray());

            // The serialized event carries the crashed session's fields.
            string json = JsonSerializer.SerializeEvent(e);
            StringAssert.Contains("\"type\":\"crash\"", json);
            StringAssert.Contains("\"exit_reason\":\"CRASH_NATIVE\"", json);
            StringAssert.Contains("\"foreground\":true", json);
            StringAssert.Contains("\"details\":{\"detected_by\":\"exit_info\"", json);
            StringAssert.Contains("\"ctx\":{\"roomId\":\"r-12\"}", json);
        }

        private static Dictionary<string, string> ToDict(string[] pairs)
        {
            var d = new Dictionary<string, string>();
            for (int i = 0; i + 1 < pairs.Length; i += 2)
                d[pairs[i]] = pairs[i + 1];
            return d;
        }

        [Test]
        public void Report_ClassifiesExitsAndSkipsBackgroundKills()
        {
            var cases = new[]
            {
                (reason: ExitReasons.Anr, fg: false, importance: 400, want: "anr"),
                (reason: ExitReasons.Crash, fg: false, importance: 400, want: "java"),
                (reason: ExitReasons.LowMemory, fg: true, importance: 100, want: "oom"),
                (reason: ExitReasons.LowMemory, fg: false, importance: 400, want: (string)null),
                (reason: ExitReasons.LowMemory, fg: false, importance: 200, want: "oom"), // visible behind a dialog
                (reason: ExitReasons.Signaled, fg: true, importance: 100, want: "killed"),
                (reason: ExitReasons.Signaled, fg: false, importance: 400, want: (string)null),
                (reason: ExitReasons.UserRequested, fg: true, importance: 100, want: "killed"),
                (reason: ExitReasons.ExcessiveResourceUsage, fg: true, importance: 100, want: "killed"),
                (reason: ExitReasons.ExitSelf, fg: true, importance: 100, want: (string)null),
                (reason: ExitReasons.PackageUpdated, fg: true, importance: 100, want: (string)null),
            };
            foreach (var c in cases)
            {
                var j = new JournalRecord { Foreground = c.fg };
                var got = CrashReporter.Classify(j, new ExitRecord { Reason = c.reason, Importance = c.importance }, out _);
                Assert.AreEqual(c.want, got, ExitReasons.Name(c.reason) + " fg=" + c.fg + " importance=" + c.importance);
            }
            Assert.AreEqual("unknown", CrashReporter.Classify(new JournalRecord { Foreground = true }, null, out _));
            Assert.IsNull(CrashReporter.Classify(new JournalRecord { Foreground = false }, null, out _));
        }

        [Test]
        public void Report_WithoutExitInfo_ForegroundIsUnknownBackgroundIsNothing()
        {
            PreviousProcess(100, 1000, true);
            PreviousProcess(200, 2000, false);
            var sink = new List<CrashEvent>();
            var reporter = Reporter(null, sink);
            reporter.ProcessPrevious(1, 1);
            Assert.AreEqual(1, sink.Count);
            Assert.AreEqual("unknown", sink[0].crash_type);
            Assert.AreEqual("journal", ToDict(sink[0].details)["detected_by"]);
            Assert.AreEqual(1, reporter.NotCrashes);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length, "both journals are done with");
        }

        [Test]
        public void Report_KeepsJournalsUntilTheEventsAreOnDisk_AndTheIdIsStable()
        {
            PreviousProcess(100, 1000, true);
            var first = new List<CrashEvent>();
            Reporter(null, first, persisted: false).ProcessPrevious(1, 1);
            Assert.AreEqual(1, first.Count);
            Assert.AreEqual(1, Directory.GetFiles(_dir).Length, "not persisted: kept for the next launch");
            var second = new List<CrashEvent>();
            Reporter(null, second).ProcessPrevious(1, 1);
            Assert.AreEqual(first[0].event_id, second[0].event_id, "a re-report is a duplicate the server drops");
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length);
        }

        [Test]
        public void Report_JournalOfAnotherDeviceId_IsDeletedNotReported()
        {
            PreviousProcess(100, 1000, true);
            var exits = new FakeExits();
            exits.Records.Add(new ExitRecord { Pid = 100, TimestampMs = 2000, Reason = ExitReasons.Anr });
            exits.Traces[100] = Encoding.UTF8.GetBytes("\"main\" prio=5 tid=1 Native\n  at a.B.c(B.java:1)\n");
            var sink = new List<CrashEvent>();
            var reporter = Reporter(exits, sink);
            reporter.CurrentDeviceId = () => "a-new-device-id-after-a-reset";
            reporter.ProcessPrevious(1, 1);
            Assert.AreEqual(0, sink.Count);
            Assert.AreEqual(1, reporter.NotCrashes);
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length, "the journal and the copied trace are gone");
        }

        [Test]
        public void Report_WithoutConsent_EnqueuesNothingAndKeepsTheJournals()
        {
            PreviousProcess(100, 1000, true);
            var sink = new List<CrashEvent>();
            Reporter(null, sink, allowed: false).ProcessPrevious(1, 1);
            Assert.AreEqual(0, sink.Count);
            Assert.AreEqual(1, Directory.GetFiles(_dir).Length);
        }

        [Test]
        public void Report_AnrWithTrace_AndACrashLoopReportsTheNewestFive()
        {
            for (int i = 0; i < 8; i++)
                PreviousProcess(100 + i, 1000 + i, true);
            var exits = new FakeExits();
            exits.Records.Add(new ExitRecord { Pid = 107, TimestampMs = 5000, Reason = ExitReasons.Anr, Description = "user request after error: Input dispatching timed out" });
            exits.Traces[107] = Encoding.UTF8.GetBytes("\"main\" prio=5 tid=1 Native\n  at com.unity3d.player.UnityPlayer.nativeRender(Native Method)\n\"UnityMain\" prio=5 tid=20 Native\n  native: #00 pc 0000000000001000  /data/app/x/lib/arm64/libil2cpp.so (BuildId: abcdef01)\n");
            var sink = new List<CrashEvent>();
            Reporter(exits, sink).ProcessPrevious(1, 1);
            Assert.AreEqual(CrashReporter.MaxJournalsProcessed, sink.Count);
            var anr = sink.Single(e => e.crash_type == "anr");
            Assert.AreEqual("ANR", anr.exit_reason);
            Assert.AreEqual("Thread \"main\" tid=1 crashed\nat com.unity3d.player.UnityPlayer.nativeRender(Native Method)\nThread \"UnityMain\" tid=20\n#00 pc 0000000000001000 libil2cpp.so (BuildId: abcdef01)\n", anr.stack);
            Assert.AreEqual(4, sink.Count(e => e.crash_type == "unknown"));
            Assert.AreEqual(0, Directory.GetFiles(_dir).Length, "the older journals of the loop are deleted unreported");
        }
    }
}
