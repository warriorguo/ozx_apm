using System;
using System.Collections.Generic;
using System.Linq;
using System.Runtime.CompilerServices;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-3: capture on any thread, unhandled exceptions, rate limits, the
    /// distribution over game contexts after de-duplication, and the
    /// per-session error sequence.
    /// </summary>
    [TestFixture]
    public class ExceptionCaptureTests
    {
        private ExceptionCollector _collector;
        private List<ExceptionEvent> _events;
        private SessionClock _clock;

        private const string Stack = "Ozx.Rooms.RoomManager:EnterRoom(Int32)\nOzx.Core.Game:Update()\n";

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            _events = new List<ExceptionEvent>();
            _clock = new SessionClock();
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 5000);
            _collector = NewCollector(_events);
        }

        [TearDown]
        public void TearDown()
        {
            GameContext.ResetForTests();
        }

        private ExceptionCollector NewCollector(List<ExceptionEvent> sink)
        {
            var c = new ExceptionCollector(new ApmConfig(), null);
            c.EnqueueForTests = e =>
            {
                lock (sink)
                    sink.Add(e);
            };
            c.ClockForTests = _clock;
            c.ActivateForTests();
            return c;
        }

        private void Error(string message) => _collector.HandleLogMessage(message, Stack, LogType.Error);

        private static string RoomError(int roomId) =>
            "[GameEventBus] Error handling event RoomEnteredEvent: System.InvalidOperationException: Room template not found for roomId=" + roomId + "\n" +
            "  at Ozx.Rooms.RoomLoader.LoadTemplate (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n" +
            "  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in <00000000000000000000000000000000>:0 \n";

        private static string PlainError(int kind) => "Asset not found: anim/kind_" + new string((char)('a' + kind % 26), 1 + kind / 26);

        private static long Sum(IEnumerable<ExceptionEvent> events) => events.Sum(e => (long)e.count);

        // ---- Context distribution after de-duplication ----

        // APM-1 verification 2 / APM-11: the same exception in 3 rooms within one
        // window gives one event per room, with that room's count.
        [Test]
        public void Repeats_InThreeRooms_AreCountedPerRoom()
        {
            GameContext.Set("levelId", "3");
            GameContext.Set("roomId", "r-1");
            Error(RoomError(1));            // first occurrence, sent at once
            for (int i = 0; i < 4; i++) Error(RoomError(1));
            GameContext.Set("roomId", "r-2");
            for (int i = 0; i < 7; i++) Error(RoomError(2));
            GameContext.Set("roomId", "r-3");
            for (int i = 0; i < 2; i++) Error(RoomError(3));
            GameContext.Set("roomId", "r-1");
            Error(RoomError(1));

            Assert.AreEqual(1, _events.Count);
            Assert.AreEqual("r-1", Ctx(_events[0], "roomId"));
            _collector.FlushPending();

            var repeats = _events.Skip(1).ToList();
            Assert.AreEqual(3, repeats.Count, "one event per room");
            var byRoom = repeats.ToDictionary(e => Ctx(e, "roomId"), e => e.count);
            Assert.AreEqual(5, byRoom["r-1"], "4 repeats before and 1 after the other rooms");
            Assert.AreEqual(7, byRoom["r-2"]);
            Assert.AreEqual(2, byRoom["r-3"]);
            Assert.AreEqual(15, Sum(_events), "every occurrence counted once");
            foreach (var e in repeats)
            {
                Assert.AreEqual(_events[0].fingerprint, e.fingerprint);
                Assert.AreEqual(_events[0].message, e.message, "repeats carry the first occurrence's text");
                Assert.AreEqual("3", Ctx(e, "levelId"));
                Assert.IsFalse(e.ctx_merged);
                Assert.IsNull(e.breadcrumbs);
            }
        }

        // More contexts than kept apart: the rest is merged into one count that
        // keeps only the entries all its occurrences shared.
        [Test]
        public void ManyContexts_OverflowIsMerged_KeepingSharedEntries()
        {
            GameContext.Set("dataVersion", "7");
            int rooms = ExceptionCollector.MaxBucketsPerFingerprint + 5;
            Error(RoomError(0));
            for (int r = 0; r < rooms; r++)
            {
                GameContext.Set("roomId", "r-" + r);
                Error(RoomError(r));
                Error(RoomError(r));
            }
            _collector.FlushPending();

            var repeats = _events.Skip(1).ToList();
            Assert.AreEqual(ExceptionCollector.MaxBucketsPerFingerprint + 1, repeats.Count);
            var merged = repeats.Single(e => e.ctx_merged);
            Assert.AreEqual(2 * 5, merged.count);
            Assert.AreEqual("7", Ctx(merged, "dataVersion"), "an entry all of them had is kept");
            Assert.IsNull(Ctx(merged, "roomId"), "an entry that varied is dropped");
            foreach (var e in repeats.Where(e => !e.ctx_merged))
                Assert.AreEqual(2, e.count);
            Assert.AreEqual(1 + 2 * rooms, Sum(_events));
        }

        [Test]
        public void Buckets_AreBoundedPerWindow()
        {
            int fingerprints = ExceptionCollector.MaxBucketsPerWindow / 4 + 3;
            for (int f = 0; f < fingerprints; f++)
            {
                for (int r = 0; r < 4; r++)
                {
                    GameContext.Set("roomId", "r-" + r);
                    Error(PlainError(f));
                    Error(PlainError(f));
                }
            }
            _collector.FlushPending();
            int countEvents = _events.Count(e => e.breadcrumbs == null);
            Assert.LessOrEqual(countEvents, ExceptionCollector.MaxBucketsPerWindow + fingerprints);
            Assert.AreEqual(fingerprints * 8, Sum(_events));
        }

        // ---- Rate limits ----

        [Test]
        public void ImmediateBudget_LaterFirstOccurrencesComeAtTheEndOfTheWindow()
        {
            int n = ExceptionCollector.MaxImmediatePerWindow + 5;
            for (int f = 0; f < n; f++)
                Error(PlainError(f));
            Assert.AreEqual(ExceptionCollector.MaxImmediatePerWindow, _events.Count);
            Assert.IsTrue(_events.All(e => e.breadcrumbs != null && e.count == 1));

            _collector.FlushPending();
            Assert.AreEqual(n, _events.Count, "the first occurrence is never lost");
            var late = _events.Skip(ExceptionCollector.MaxImmediatePerWindow).ToList();
            Assert.IsTrue(late.All(e => e.count == 1 && e.breadcrumbs == null));
            CollectionAssert.AreEqual(Enumerable.Range(ExceptionCollector.MaxImmediatePerWindow + 1, 5), late.Select(e => e.error_seq),
                "they keep their own sequence number");
            Assert.AreEqual(n, _events.Select(e => e.fingerprint).Distinct().Count());
        }

        [Test]
        public void FingerprintLimit_TheRestIsCountedInOneSummary()
        {
            int n = ExceptionCollector.MaxFingerprintsPerWindow + 20;
            for (int f = 0; f < n; f++)
            {
                Error(PlainError(f));
                Error(PlainError(f));
            }
            _collector.FlushPending();

            Assert.AreEqual(2L * n, Sum(_events), "every occurrence is counted");
            Assert.AreEqual(ExceptionCollector.MaxFingerprintsPerWindow + 1, _events.Select(e => e.fingerprint).Distinct().Count());
            var summary = _events.Single(e => e.message.StartsWith("[OzxApm] ", StringComparison.Ordinal));
            Assert.AreEqual(40, summary.count);
            StringAssert.StartsWith("[OzxApm] 40 errors of 20 kinds not reported one by one", summary.message);
            Assert.AreEqual(2 * ExceptionCollector.MaxFingerprintsPerWindow + 1, summary.error_seq, "sequence of the first one summarized");
            Assert.AreEqual(ExceptionParser.Analyze("error", summary.message, "", "").Fingerprint, summary.fingerprint);
            _collector.GetStats(out long seen, out long events, out long untracked);
            Assert.AreEqual(2L * n, seen);
            Assert.AreEqual(_events.Count, events);
            Assert.AreEqual(40, untracked);

            // The next window tracks them again.
            _events.Clear();
            Error(PlainError(n - 1));
            Assert.AreEqual(1, _events.Count);
        }

        [Test]
        public void Window_EndsInUpdate_AndIdleUpdateEnqueuesNothing()
        {
            Error(PlainError(1));
            Error(PlainError(1));
            _collector.Update();
            Assert.AreEqual(1, _events.Count);
            _collector.ExpireWindowForTests();
            _collector.Update();
            Assert.AreEqual(2, _events.Count);
            Assert.AreEqual(1, _events[1].count);

            _collector.ExpireWindowForTests();
            _collector.Update();
            Assert.AreEqual(2, _events.Count);
            Error(PlainError(1));
            Assert.AreEqual(3, _events.Count, "a new window starts with a first occurrence again");
        }

        // ---- Per-session error sequence ----

        [Test]
        public void ErrorSequence_CountsEveryOccurrenceInTheSession()
        {
            Error("Awake failed: save file missing");      // root cause: 1
            for (int i = 0; i < 30; i++)
                Error(PlainError(i % 3));                   // cascade: 2..31
            _collector.FlushPending();

            var root = _events[0];
            Assert.AreEqual(1, root.error_seq);
            Assert.That(root.session_ms, Is.InRange(4000L, 60000L));
            var cascadeFirsts = _events.Where(e => e.breadcrumbs != null).Skip(1).ToList();
            CollectionAssert.AreEqual(new[] { 2, 3, 4 }, cascadeFirsts.Select(e => e.error_seq));
            var repeats = _events.Where(e => e.breadcrumbs == null).ToList();
            CollectionAssert.AreEqual(new[] { 5, 6, 7 }, repeats.Select(e => e.error_seq).OrderBy(s => s),
                "a repeat count carries the sequence number of its first repeat");
            Assert.AreEqual(31, Sum(_events));
            Assert.AreEqual(1, _events.Count(e => e.error_seq == 1), "exactly one first error per session");
        }

        [Test]
        public void ErrorSequence_RestartsWithANewSession()
        {
            Error(PlainError(1));
            Error(PlainError(2));
            _collector.FlushPending();
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds());
            Error(PlainError(3));
            Assert.AreEqual(1, _events.Last().error_seq);
            Assert.That(_events.Last().session_ms, Is.LessThan(5000L));
        }

        // ---- Threads ----

        [Test]
        public void ErrorsOnManyThreads_AreAllCounted()
        {
            const int threads = 8, perThread = 2000;
            var start = new ManualResetEvent(false);
            var workers = new List<Thread>();
            for (int t = 0; t < threads; t++)
            {
                int id = t;
                var thread = new Thread(() =>
                {
                    start.WaitOne();
                    for (int i = 0; i < perThread; i++)
                    {
                        _collector.HandleLogMessage(RoomError(i), Stack, LogType.Error);              // one group, every thread
                        _collector.HandleLogMessage(PlainError(id), Stack, LogType.Error);            // one group per thread
                        if (i % 100 == 0)
                            _collector.HandleLogMessage("a warning " + i, "", LogType.Warning);
                    }
                });
                thread.Start();
                workers.Add(thread);
            }
            start.Set();
            // The main thread keeps closing windows meanwhile.
            for (int k = 0; k < 20; k++)
            {
                _collector.FlushPending();
                Thread.Sleep(1);
            }
            foreach (var w in workers)
                Assert.IsTrue(w.Join(30000));
            _collector.FlushPending();

            Assert.AreEqual(2L * threads * perThread, Sum(_events));
            Assert.AreEqual(threads + 1, _events.Select(e => e.fingerprint).Distinct().Count());
            var seqs = _events.Select(e => e.error_seq).ToList();
            Assert.AreEqual(seqs.Count, seqs.Distinct().Count(), "sequence numbers are unique");
        }

        // An exception logged off the main thread waits HoldMs (an unhandled
        // report may still mark it); main-thread logs are never held.
        [Test]
        public void ExceptionLoggedOffTheMainThread_IsHeldBriefly()
        {
            RunOnThread(() => _collector.HandleLogMessage("InvalidOperationException: boom", "Ozx.Net.Worker.Run () (at Assets/Net/Worker.cs:10)\n", LogType.Exception));
            Assert.AreEqual(0, _events.Count);
            _collector.ReleaseHeldForTests();
            Assert.AreEqual(1, _events.Count);
            Assert.AreEqual("exception", _events[0].log_type);
            Assert.IsNull(_events[0].unhandled);

            RunOnThread(() => _collector.HandleLogMessage("Asset not found: x", Stack, LogType.Error));
            Assert.AreEqual(2, _events.Count, "errors are not held");

            // Held at the end of the window: enqueued with it, counted once.
            RunOnThread(() => _collector.HandleLogMessage("ArgumentException: bad", "Ozx.Net.Worker.Send () (at Assets/Net/Worker.cs:20)\n", LogType.Exception));
            _collector.FlushPending();
            Assert.AreEqual(3, _events.Count);
            _collector.GetStats(out long seen, out long events, out _);
            Assert.AreEqual(3, seen);
            Assert.AreEqual(3, events);
        }

        // ---- Unhandled exceptions ----

        [Test]
        public void UnhandledException_OnAThread_IsReportedOnce_AndUnitysLogOfItIsItsEcho()
        {
            var ex = Capture(() => ApmTestGame.Throwers.Explode(7));
            RunOnThread(() =>
            {
                _collector.OnUnhandledForTests(ex, false);
                // Unity's UnhandledExceptionHandler logs it after us on some platforms.
                _collector.HandleLogMessage(UnityMessage(ex), UnityStack(ex), LogType.Exception);
            });
            _collector.ReleaseHeldForTests();
            _collector.FlushPending();

            Assert.AreEqual(1, Sum(_events));
            var e = _events.Single();
            Assert.AreEqual("domain", e.unhandled);
            Assert.AreEqual("error", e.log_type);
            StringAssert.StartsWith("System.InvalidOperationException: exploded 7", e.message);
            StringAssert.StartsWith("System.InvalidOperationException: exploded 7", e.embedded_exception);
            var parsed = ExceptionParser.Analyze(e.log_type, e.message, e.stack, e.embedded_exception);
            StringAssert.Contains("ApmTestGame.Throwers.Inner", parsed.ThrowSite);
            Assert.AreEqual(e.fingerprint, parsed.Fingerprint);
        }

        [Test]
        public void UnhandledException_LoggedByUnityFirst_MarksThatOccurrence()
        {
            var ex = Capture(() => ApmTestGame.Throwers.Explode(8));
            Assert.AreEqual(
                ExceptionParser.Analyze("exception", UnityMessage(ex), UnityStack(ex), "").Fingerprint,
                ExceptionParser.Analyze("error", "x", "", ex.ToString()).Fingerprint,
                "Unity's log of an exception and its ToString group together");

            RunOnThread(() =>
            {
                _collector.HandleLogMessage(UnityMessage(ex), UnityStack(ex), LogType.Exception);
                _collector.OnUnhandledForTests(ex, false);
            });
            Assert.AreEqual(1, _events.Count, "released at once when marked");
            Assert.AreEqual("domain", _events[0].unhandled);
            Assert.AreEqual("exception", _events[0].log_type);

            // A repeat in the same window: its count moves to an unhandled bucket.
            RunOnThread(() =>
            {
                _collector.HandleLogMessage(UnityMessage(ex), UnityStack(ex), LogType.Exception);
                _collector.OnUnhandledForTests(ex, false);
            });
            _collector.FlushPending();
            Assert.AreEqual(2, Sum(_events));
            Assert.IsTrue(_events.Where(e => e.count > 0).All(e => e.unhandled == "domain"));
        }

        [Test]
        public void TerminatingException_IsWrittenBeforeReturning()
        {
            int persisted = 0;
            _collector.PersistNow = () => persisted = _events.Count;
            Error(PlainError(1));
            Error(PlainError(1));
            var ex = Capture(() => ApmTestGame.Throwers.Explode(9));
            RunOnThread(() => _collector.OnUnhandledForTests(ex, true));

            Assert.AreEqual(3, persisted, "pending counts and the exception were enqueued before PersistNow");
            Assert.AreEqual(1, _events.Count(e => e.unhandled == "domain_terminating"));
        }

        [Test]
        public void UnobservedTask_SingleException_IsReportedAsItself()
        {
            var ex = Capture(() => ApmTestGame.Throwers.Explode(10));
            RunOnThread(() => _collector.OnUnobservedForTests(new AggregateException(ex)));
            var e = _events.Single();
            Assert.AreEqual("task", e.unhandled);
            StringAssert.StartsWith("System.InvalidOperationException: exploded 10", e.message);

            RunOnThread(() => _collector.OnUnobservedForTests(new AggregateException(ex, new ArgumentException("second"))));
            StringAssert.StartsWith("System.AggregateException", _events.Last().message);
        }

        // ---- Cost ----

        [Test]
        public void RepeatInAKnownContext_DoesNotAllocate()
        {
            GameContext.Set("roomId", "r-1");
            string message = RoomError(5);
            Error(message);
            Error(message);
            Assert.That(() => _collector.HandleLogMessage(message, Stack, LogType.Error), Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void IdleUpdate_DoesNotAllocate()
        {
            _collector.Update();
            Assert.That(() => _collector.Update(), Is.Not.AllocatingGCMemory());
        }

        // ---- Helpers ----

        private static string Ctx(ExceptionEvent e, string key) =>
            e.ctx != null && e.ctx.TryGetValue(key, out string v) ? v : null;

        private static void RunOnThread(Action action)
        {
            Exception error = null;
            var t = new Thread(() =>
            {
                try { action(); }
                catch (Exception ex) { error = ex; }
            });
            t.Start();
            Assert.IsTrue(t.Join(10000));
            if (error != null)
                throw new Exception("thread failed", error);
        }

        private static Exception Capture(Action a)
        {
            try
            {
                a();
            }
            catch (Exception ex)
            {
                return ex;
            }
            throw new InvalidOperationException("did not throw");
        }

        // What Unity's log callback gets for Debug.LogException(ex): the short
        // type and message, and the frames in Unity's format.
        private static string UnityMessage(Exception ex) => ex.GetType().Name + ": " + ex.Message;

        private static string UnityStack(Exception ex)
        {
            var lines = new List<string>();
            foreach (var raw in ex.ToString().Split('\n'))
            {
                string line = raw.Trim();
                if (!line.StartsWith("at ", StringComparison.Ordinal))
                    continue;
                line = line.Substring(3);
                int bracket = line.IndexOf(" [0x", StringComparison.Ordinal);
                int inAt = line.IndexOf(" in ", StringComparison.Ordinal);
                string method = bracket > 0 ? line.Substring(0, bracket) : inAt > 0 ? line.Substring(0, inAt) : line;
                string location = inAt > 0 ? line.Substring(inAt + 4) : "";
                lines.Add(method + (location.Length > 0 ? " (at " + location + ")" : ""));
            }
            return string.Join("\n", lines) + "\n";
        }
    }
}

namespace ApmTestGame
{
    internal static class Throwers
    {
        [MethodImpl(MethodImplOptions.NoInlining)]
        internal static void Explode(int n)
        {
            Inner(n);
        }

        [MethodImpl(MethodImplOptions.NoInlining)]
        private static void Inner(int n)
        {
            throw new InvalidOperationException("exploded " + n);
        }
    }
}
