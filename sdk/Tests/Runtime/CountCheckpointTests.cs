using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-24: the counts collectors keep in memory until their window closes
    /// (exception repeats, custom events beyond the budget, metric series) are
    /// checkpointed by the storage worker and reach the server after the
    /// process is killed in the foreground, exactly once (the checkpoint copy
    /// and the final count share their event id). The storage worker is driven
    /// with Pump(); a kill is OfflineStorage.SimulateCrashForTests.
    /// </summary>
    [TestFixture]
    public class CountCheckpointTests
    {
        private string _dir;
        private ApmConfig _config;
        private SessionClock _clock;
        private readonly List<OfflineStorage> _stores = new List<OfflineStorage>();

        private const string Stack = "ApmTestDriver:Update()\n";

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_cp_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig
            {
                BatchSize = 50,
                MaxBatchBytes = 64 * 1024,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                EnableCompression = false,
                LogLevel = LogLevel.None
            };
            _clock = new SessionClock();
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 5000, "s-1");
        }

        [TearDown]
        public void TearDown()
        {
            OfflineStorage.BeforeWriteHook = null;
            foreach (var store in _stores)
                store.Shutdown(0);
            _stores.Clear();
            GameContext.ResetForTests();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        // ---- Helpers ----

        private OfflineStorage NewStore()
        {
            var store = new OfflineStorage(_config, _dir, false);
            store.CheckpointStamp = Stamp;
            _stores.Add(store);
            return store;
        }

        private static void Stamp(BaseEvent e)
        {
            e.app_version = "1.0.0";
            e.platform = "Android";
            if (string.IsNullOrEmpty(e.session_id))
                e.session_id = "s-1";
            e.device_id = "d";
        }

        /// <summary>The process dies: no flush, no seal, no checkpoint deletion.</summary>
        private void Kill(OfflineStorage store)
        {
            store.SimulateCrashForTests();
            _stores.Remove(store);
        }

        private string[] CheckpointFiles() => Directory.Exists(_dir)
            ? Directory.GetFiles(_dir).Where(f => f.Contains(".ckpt")).OrderBy(f => f, StringComparer.Ordinal).ToArray()
            : new string[0];

        private static string Body(UploadBatch batch)
        {
            if (!batch.Compressed)
                return Encoding.UTF8.GetString(batch.Payload);
            using (var input = new GZipStream(new MemoryStream(batch.Payload), CompressionMode.Decompress))
            using (var reader = new StreamReader(input, Encoding.UTF8))
                return reader.ReadToEnd();
        }

        /// <summary>Every event the store would upload (all batches, confirmed one by one).</summary>
        private static List<string> UploadAll(OfflineStorage store)
        {
            var lines = new List<string>();
            store.Flush(); // seal what a live store has open
            store.Pump();
            for (int i = 0; i < 100; i++)
            {
                store.RequestUpload();
                store.Pump();
                if (store.PollUpload(out var batch) != UploadPoll.Ready || batch == null)
                    break;
                string body = Body(batch);
                lines.AddRange(SplitEvents(body));
                store.CompleteUpload(batch, UploadOutcome.Delivered);
                store.Pump();
            }
            return lines;
        }

        // {"events":[{...},{...}]} -> one string per event (no nested '},{' in these tests).
        private static IEnumerable<string> SplitEvents(string body)
        {
            string inner = body.Substring("{\"events\":[".Length, body.Length - "{\"events\":[".Length - 2);
            return Regex.Split(inner, @"(?<=\}),(?=\{""type"")");
        }

        private static string Id(string line) => Regex.Match(line, "\"event_id\":\"([0-9a-f]*)\"").Groups[1].Value;
        private static long Count(string line) => long.Parse(Regex.Match(line, "\"count\":(\\d+)").Groups[1].Value, CultureInfo.InvariantCulture);
        private static bool Recovered(string line) => line.Contains("\"recovered\":true");
        private static long Seq(string line) => long.Parse(Regex.Match(line, "\"seq\":(\\d+)").Groups[1].Value, CultureInfo.InvariantCulture);

        private sealed class FakeSource : ICountCheckpointSource
        {
            public Func<List<BaseEvent>> Pending = () => new List<BaseEvent>();
            public bool Dirty = true;
            public bool Busy;
            public Action OnCollect;
            public int Calls;
            public bool LastForce;

            public string CheckpointName => "fake";

            public CheckpointCollect CollectCheckpoint(List<BaseEvent> into, bool force)
            {
                Calls++;
                LastForce = force;
                OnCollect?.Invoke();
                if (Busy)
                    return CheckpointCollect.Busy;
                if (!Dirty && !force)
                    return CheckpointCollect.Unchanged;
                Dirty = false;
                into.AddRange(Pending());
                return CheckpointCollect.Collected;
            }
        }

        private static ExceptionEvent Count(string id, int count)
        {
            return new ExceptionEvent
            {
                event_id = id,
                timestamp = 1700000000000,
                fingerprint = "f",
                message = "APMT k3-n 1 1: test error",
                stack = Stack,
                count = count,
                log_type = "error"
            };
        }

        private (OfflineStorage store, FakeSource source) StoreWithFake(params ExceptionEvent[] pending)
        {
            var store = NewStore();
            var source = new FakeSource
            {
                Pending = () => pending.Select(p => (BaseEvent)Count(p.event_id, p.count)).ToList()
            };
            store.AddCheckpointSource(source);
            return (store, source);
        }

        private ExceptionCollector NewExceptionCollector(OfflineStorage store, List<bool> requests = null)
        {
            var c = new ExceptionCollector(new ApmConfig(), null);
            c.EnqueueForTests = e =>
            {
                Stamp(e);
                store.Enqueue(e);
            };
            c.CheckpointRequestedForTests = urgent =>
            {
                requests?.Add(urgent);
                store.RequestCheckpoint(urgent);
            };
            c.ClockForTests = _clock;
            c.ActivateForTests();
            store.AddCheckpointSource(c);
            return c;
        }

        private CustomCollector NewCustomCollector(OfflineStorage store, ApmConfig config)
        {
            var c = new CustomCollector(config, null, new Timeline());
            c.EnqueueForTests = e =>
            {
                Stamp(e);
                store.Enqueue(e);
            };
            c.CheckpointRequestedForTests = store.RequestCheckpoint;
            c.ClockForTests = _clock;
            c.ActivateForTests();
            store.AddCheckpointSource(c);
            return c;
        }

        // ---- Storage ----

        [Test]
        public void Checkpoint_IsUploadedByTheNextLaunch_AsTheSameEvent()
        {
            var (store, _) = StoreWithFake(Count("aaaa", 57));
            store.RequestCheckpoint(false);
            store.Pump();
            var files = CheckpointFiles();
            Assert.AreEqual(1, files.Length);
            StringAssert.EndsWith("counts-fake.ckpt", files[0]);
            string line = File.ReadAllText(files[0]).TrimEnd('\n');
            Assert.AreEqual("aaaa", Id(line));
            Assert.AreEqual(57, Count(line));
            Assert.AreEqual(0, Seq(line), "a copy takes no sequence number");
            Assert.IsTrue(Recovered(line));
            StringAssert.Contains("\"device_id\":\"d\"", line, "stamped like a queued event");
            Assert.AreEqual(1, store.CheckpointWrites);
            Kill(store);

            var next = NewStore();
            next.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length, "became a segment");
            Assert.AreEqual(1, next.CheckpointsRecovered);
            var uploaded = UploadAll(next);
            Assert.AreEqual(1, uploaded.Count);
            Assert.AreEqual("aaaa", Id(uploaded[0]));
            Assert.AreEqual(57, Count(uploaded[0]));
            Assert.IsTrue(Recovered(uploaded[0]));
        }

        [Test]
        public void Checkpoint_AtMostOncePerInterval_AndOnlyWhenChanged()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 1));
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(1, store.CheckpointWrites, "the first one at once");

            source.Dirty = true;
            store.RequestCheckpoint(false);
            store.Pump();
            store.Pump();
            Assert.AreEqual(1, store.CheckpointWrites, "not before the interval");
            Assert.AreEqual(1, source.Calls);

            store.ExpireCheckpointIntervalForTests();
            store.Pump();
            Assert.AreEqual(2, store.CheckpointWrites, "due now");

            // Nothing requested: nothing collected, however long.
            store.ExpireCheckpointIntervalForTests();
            store.Pump();
            Assert.AreEqual(2, source.Calls);

            // Requested but unchanged (the source says so): the file stays.
            store.RequestCheckpoint(false);
            store.ExpireCheckpointIntervalForTests();
            store.Pump();
            Assert.AreEqual(3, source.Calls);
            Assert.AreEqual(2, store.CheckpointWrites);
            Assert.AreEqual(1, CheckpointFiles().Length);

            // Urgent (a window closed): at once, whatever the interval.
            source.Dirty = true;
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(3, store.CheckpointWrites);
        }

        [Test]
        public void NothingPending_DeletesTheFile()
        {
            var store = NewStore();
            var pending = new List<ExceptionEvent> { Count("aaaa", 3) };
            var source = new FakeSource { Pending = () => pending.Select(p => (BaseEvent)Count(p.event_id, p.count)).ToList() };
            store.AddCheckpointSource(source);
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(1, CheckpointFiles().Length);

            pending.Clear(); // the window closed
            source.Dirty = true;
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            Kill(store);
            var next = NewStore();
            Assert.AreEqual(0, UploadAll(next).Count);
        }

        // Busy (a window's events are being queued): not retried by the storage
        // (a close that never completed would make it poll every 10 ms); the
        // close asks for an urgent checkpoint once its events are queued.
        [Test]
        public void Busy_WaitsForTheClosesRequest()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            source.Busy = true;
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            source.Busy = false;
            store.ExpireCheckpointIntervalForTests();
            store.Pump();
            Assert.AreEqual(1, source.Calls, "no retry of its own");
            store.RequestCheckpoint(true); // EnqueueAll's request
            store.Pump();
            Assert.AreEqual(1, CheckpointFiles().Length);
        }

        // Counts made without consent: the dirty flags stay set and nobody asks
        // again, so the storage asks every source at the next collection.
        [Test]
        public void AfterConsentIsGrantedAgain_EverySourceIsAsked()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            _config.SetConsentState(ConsentCategory.Technical, ConsentState.Undecided);
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(0, source.Calls);
            _config.SetConsentState(ConsentCategory.Technical, ConsentState.Granted);
            source.Dirty = false; // its own flag: it would answer Unchanged
            store.RequestCheckpoint(false); // ApmClient, when collection resumes
            store.ExpireCheckpointIntervalForTests();
            store.Pump();
            Assert.IsTrue(source.LastForce);
            Assert.AreEqual(1, CheckpointFiles().Length);
        }

        // Turned off at run time: the file (its windows are sent anyway) goes.
        [Test]
        public void IntervalSetToZeroAtRunTime_DeletesTheFile()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(1, CheckpointFiles().Length);
            // A request made just before the interval is set to 0 (afterwards
            // RequestCheckpoint is a no-op).
            store.RequestCheckpoint(true);
            _config.CountCheckpointIntervalSeconds = 0f;
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
        }

        // Clear (device id reset) between the collection and the write: the
        // copies were stamped before it; they are collected again (force).
        [Test]
        public void ClearDuringTheCollection_DiscardsIt_AndCollectsAgain()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            bool cleared = false;
            source.OnCollect = () =>
            {
                if (cleared)
                    return;
                cleared = true;
                store.Clear(true);
            };
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(0, store.CheckpointWrites);
            source.Dirty = false; // unchanged since: still collected (forced)
            store.ExpireCheckpointIntervalForTests(); // at the next due time
            store.Pump();
            Assert.IsTrue(source.LastForce);
            Assert.AreEqual(1, store.CheckpointWrites);
            Assert.AreEqual(1, CheckpointFiles().Length);
        }

        [Test]
        public void WithoutConsent_NothingIsWritten_AndDeniedDeletesTheFiles()
        {
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            store.RequestCheckpoint(false);
            store.Pump();
            Assert.AreEqual(1, CheckpointFiles().Length);

            _config.SetConsentState(ConsentCategory.Technical, ConsentState.Undecided);
            source.Dirty = true;
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(1, store.CheckpointWrites, "nothing new written");
            Assert.AreEqual(1, CheckpointFiles().Length, "withdrawn consent keeps what is stored");

            _config.SetConsentState(ConsentCategory.Technical, ConsentState.Denied);
            store.Clear();
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            Assert.IsFalse(Directory.Exists(_dir), "nothing of the SDK left");
        }

        [Test]
        public void Recovery_TakesTheCheckpoint_OverItsTmp_AndATmpAlone()
        {
            Directory.CreateDirectory(_dir);
            string a = "{\"type\":\"exception\",\"event_id\":\"aaaa\",\"seq\":0,\"timestamp\":1700000000000,\"fingerprint\":\"f\",\"count\":5,\"recovered\":true}";
            string aCut = "{\"type\":\"exception\",\"event_id\":\"aaaa\",\"seq\":0,\"timestamp\":17000";
            string b = "{\"type\":\"custom_event\",\"event_id\":\"bbbb\",\"seq\":0,\"timestamp\":1700000000000,\"name\":\"n\",\"count\":9,\"aggregated\":true,\"recovered\":true}";
            File.WriteAllText(Path.Combine(_dir, "counts-exceptions.ckpt"), a + "\n");
            File.WriteAllText(Path.Combine(_dir, "counts-exceptions.ckpt.tmp"), aCut);
            File.WriteAllText(Path.Combine(_dir, "counts-custom.ckpt.tmp"), b + "\n");

            var store = NewStore();
            var uploaded = UploadAll(store);
            Assert.AreEqual(2, store.CheckpointsRecovered);
            Assert.AreEqual(0, CheckpointFiles().Length);
            CollectionAssert.AreEquivalent(new[] { a, b }, uploaded);
        }

        // The window's final counts did not reach the disk: the checkpoint is
        // not replaced by one without them.
        [Test]
        public void FailedEventWrite_KeepsThePreviousCheckpoint()
        {
            var store = NewStore();
            var pending = new List<ExceptionEvent> { Count("aaaa", 3) };
            var source = new FakeSource { Pending = () => pending.Select(p => (BaseEvent)Count(p.event_id, p.count)).ToList() };
            store.AddCheckpointSource(source);
            store.RequestCheckpoint(false);
            store.Pump();
            string before = File.ReadAllText(CheckpointFiles()[0]);

            pending.Clear();
            source.Dirty = true;
            store.Enqueue(Count("aaaa", 9)); // the final count
            store.RequestCheckpoint(true);
            OfflineStorage.BeforeWriteHook = s => throw new IOException("disk full");
            store.Pump();
            Assert.AreEqual(before, File.ReadAllText(CheckpointFiles()[0]));

            OfflineStorage.BeforeWriteHook = null;
            store.ResetWriteBackoffForTests();
            store.ExpireCheckpointIntervalForTests(); // collected again at the next due time
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length, "collected again once the final count is on disk");
            var uploaded = UploadAll(store);
            Assert.AreEqual(1, uploaded.Count);
            Assert.AreEqual(9, Count(uploaded[0]));
        }

        [Test]
        public void IntervalZero_TurnsCheckpointsOff()
        {
            _config.CountCheckpointIntervalSeconds = 0f;
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(0, source.Calls);
            Assert.AreEqual(0, CheckpointFiles().Length);
        }

        // Flash wear: a large checkpoint is rewritten less often (on average at
        // most CheckpointBytesPerSecond); a window close still deletes it at once.
        [Test]
        public void ALargeCheckpoint_IsRewrittenLessOften()
        {
            var big = Enumerable.Range(0, 15).Select(i =>
            {
                var e = Count("id" + i.ToString("D4", CultureInfo.InvariantCulture), 2);
                e.stack = new string('x', 4000);
                return e;
            }).ToArray();
            var store = NewStore();
            var source = new FakeSource { Pending = () => big.Select(p => { var c = Count(p.event_id, p.count); c.stack = p.stack; return (BaseEvent)c; }).ToList() };
            store.AddCheckpointSource(source);
            store.RequestCheckpoint(false);
            store.Pump();
            long bytes = store.CheckpointLastBytes;
            Assert.Greater(bytes, 60 * 1024);
            int spacing = store.CheckpointSpacingMs;
            Assert.AreEqual((int)(bytes * 1000 / OfflineStorage.CheckpointBytesPerSecond), spacing);
            Assert.Greater(spacing, store.CheckpointIntervalMs);

            source.Dirty = true;
            store.RequestCheckpoint(false);
            store.ExpireCheckpointIntervalForTests(); // 5 s: not enough for this size
            store.Pump();
            Assert.AreEqual(1, store.CheckpointWrites);
            store.ExpireCheckpointIntervalForTests(spacing + 1);
            store.Pump();
            Assert.AreEqual(2, store.CheckpointWrites);

            big = new ExceptionEvent[0]; // the window closed
            source.Dirty = true;
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            Assert.AreEqual(store.CheckpointIntervalMs, store.CheckpointSpacingMs);
        }

        [Test]
        public void MemoryOnly_NeverCheckpoints()
        {
            _config.EnableOfflineStorage = false;
            var (store, source) = StoreWithFake(Count("aaaa", 3));
            store.RequestCheckpoint(true);
            store.Pump();
            Assert.AreEqual(0, source.Calls);
            Assert.IsFalse(Directory.Exists(_dir));
        }

        // A second instance on the same directory runs memory-only (APM-2):
        // a request made before it found out is dropped, not retried forever.
        [Test]
        public void SecondInstanceOnTheDirectory_DropsTheRequest()
        {
            var owner = NewStore();
            owner.Pump();
            var (second, source) = StoreWithFake(Count("aaaa", 3));
            second.RequestCheckpoint(true);
            second.Pump();
            Assert.IsFalse(second.PersistsToDisk);
            Assert.AreEqual(0, source.Calls);
            second.Pump();
            Assert.AreEqual(0, source.Calls);
        }

        [Test]
        public void AFullCheckpoint_LeavesTheRestOut_AndCountsThem()
        {
            _config.MaxBatchBytes = 16 * 1024; // checkpoint cap 64 KB
            var big = Enumerable.Range(0, 40).Select(i =>
            {
                var e = Count("id" + i.ToString("D4", CultureInfo.InvariantCulture), 2);
                e.stack = new string('x', 4000);
                return e;
            }).ToArray();
            var store = NewStore();
            store.AddCheckpointSource(new FakeSource { Pending = () => big.Select(p => { var c = Count(p.event_id, p.count); c.stack = p.stack; return (BaseEvent)c; }).ToList() });
            store.RequestCheckpoint(false);
            store.Pump();
            long bytes = new FileInfo(CheckpointFiles()[0]).Length;
            Assert.LessOrEqual(bytes, 64 * 1024);
            Assert.Greater(store.CheckpointEventsOmitted, 0);
            Assert.AreEqual(40, File.ReadAllLines(CheckpointFiles()[0]).Length + store.CheckpointEventsOmitted);
        }

        // ---- Exception repeat counts (APM-3/4) ----

        // The K1 device scenario: an error repeated, the process killed in the
        // foreground within the window. Before APM-24 the server got count 1.
        [Test]
        public void RepeatCounts_KilledAfterACheckpoint_ReachTheNextLaunch()
        {
            var store = NewStore();
            var requests = new List<bool>();
            var collector = NewExceptionCollector(store, requests);
            for (int i = 1; i <= 50; i++)
                collector.HandleLogMessage("APMT k3-n " + i + " 1792000000000: test error", Stack, LogType.Error);
            Assert.AreEqual(1, requests.Count, "one request per checkpoint cycle, not per repeat");
            Assert.IsFalse(requests[0]);
            store.Pump(); // first occurrence on disk, checkpoint of the 49 repeats
            for (int i = 51; i <= 60; i++) // after the last checkpoint: lost
                collector.HandleLogMessage("APMT k3-n " + i + " 1792000000000: test error", Stack, LogType.Error);
            Assert.AreEqual(2, requests.Count);
            Kill(store);

            var next = NewStore();
            var uploaded = UploadAll(next);
            Assert.AreEqual(2, uploaded.Count, string.Join("\n", uploaded));
            var first = uploaded.Single(l => !Recovered(l));
            var repeats = uploaded.Single(Recovered);
            Assert.AreEqual(1, Count(first));
            Assert.AreEqual(49, Count(repeats));
            Assert.AreNotEqual(Id(first), Id(repeats));
            StringAssert.Contains("\"error_seq\":2", repeats, "the count's first occurrence");
            StringAssert.Contains("\"session_id\":\"s-1\"", repeats);
        }

        // The window closes normally: the final count has the copy's id, is
        // written before the checkpoint is deleted, and nothing is recovered.
        [Test]
        public void WindowClose_FinalCountHasTheCopysId_AndTheCheckpointGoes()
        {
            var store = NewStore();
            var requests = new List<bool>();
            var collector = NewExceptionCollector(store, requests);
            for (int i = 0; i < 20; i++)
                collector.HandleLogMessage("Asset not found: anim/x", Stack, LogType.Error);
            store.Pump();
            string copy = File.ReadAllText(CheckpointFiles().Single()).TrimEnd('\n');
            Assert.AreEqual(19, Count(copy));

            collector.HandleLogMessage("Asset not found: anim/x", Stack, LogType.Error);
            collector.FlushPending();
            Assert.IsTrue(requests.Last(), "a close asks at once");
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            Kill(store); // killed afterwards: nothing to recover

            var next = NewStore();
            var uploaded = UploadAll(next);
            Assert.AreEqual(0, next.CheckpointsRecovered);
            var final = uploaded.Single(l => Count(l) == 20);
            Assert.AreEqual(Id(copy), Id(final), "the server keeps one of them");
            Assert.IsFalse(Recovered(final));
            Assert.Greater(Seq(final), 0);
        }

        // Killed between writing the final count and deleting the checkpoint
        // (simulated by restoring the file): both are uploaded with the same
        // id; the server drops the second (APM-19).
        [Test]
        public void KilledBetweenFinalAndDelete_BothCarryTheSameId()
        {
            var store = NewStore();
            var collector = NewExceptionCollector(store);
            for (int i = 0; i < 5; i++)
                collector.HandleLogMessage("Pool not found", Stack, LogType.Error);
            store.Pump();
            string path = CheckpointFiles().Single();
            string copy = File.ReadAllText(path);
            collector.FlushPending();
            store.Pump();
            File.WriteAllText(path, copy);
            Kill(store);

            var uploaded = UploadAll(NewStore());
            var counts = uploaded.Where(l => Count(l) == 4).ToList();
            Assert.AreEqual(2, counts.Count);
            Assert.AreEqual(Id(counts[0]), Id(counts[1]));
            Assert.IsFalse(Recovered(counts[0]), "the final count is uploaded first");
            Assert.IsTrue(Recovered(counts[1]));
        }

        [Test]
        public void Summary_OfUntrackedErrors_IsCheckpointedWithItsWindowsId()
        {
            var store = NewStore();
            var collector = NewExceptionCollector(store);
            for (int k = 0; k < ExceptionCollector.MaxFingerprintsPerWindow + 3; k++)
                collector.HandleLogMessage("Asset not found: kind_" + new string((char)('a' + k % 26), 1 + k / 26), Stack, LogType.Error);
            store.Pump();
            string summary = File.ReadAllLines(CheckpointFiles().Single()).Single(l => l.Contains("not reported one by one"));
            Assert.AreEqual(3, Count(summary));
            collector.FlushPending();
            store.Pump();
            var uploaded = UploadAll(store);
            Assert.AreEqual(Id(summary), Id(uploaded.Single(l => l.Contains("not reported one by one"))));
        }

        // ---- Custom events and metrics (APM-6) ----

        [Test]
        public void OverBudgetEventsAndMetricSeries_KilledAfterACheckpoint_ReachTheNextLaunch()
        {
            var config = new ApmConfig { MaxCustomEventsPerMinute = 2 };
            var store = NewStore();
            var collector = NewCustomCollector(store, config);
            for (int i = 0; i < 30; i++)
                collector.TrackEvent("enemy_killed", null);
            for (int i = 1; i <= 40; i++)
                collector.RecordMetric("frame_ms", i, "ms");
            store.Pump();
            collector.TrackEvent("enemy_killed", null); // after the checkpoint: lost
            Kill(store);

            var uploaded = UploadAll(NewStore());
            var sent = uploaded.Where(l => l.Contains("\"custom_event\"") && !Recovered(l)).ToList();
            var aggregated = uploaded.Single(l => l.Contains("\"custom_event\"") && Recovered(l));
            var metric = uploaded.Single(l => l.Contains("\"custom_metric\""));
            Assert.AreEqual(2, sent.Count, "within the budget: on disk at once");
            Assert.AreEqual(28, Count(aggregated));
            StringAssert.Contains("\"aggregated\":true", aggregated);
            Assert.AreEqual(40, Count(metric));
            Assert.IsTrue(Recovered(metric));
            StringAssert.Contains("\"sum\":820", metric);
            StringAssert.Contains("\"min\":1", metric);
            StringAssert.Contains("\"max\":40", metric);
        }

        [Test]
        public void CustomWindowClose_FinalEventsHaveTheCopiesIds()
        {
            var config = new ApmConfig { MaxCustomEventsPerMinute = 1 };
            var store = NewStore();
            var collector = NewCustomCollector(store, config);
            for (int i = 0; i < 5; i++)
                collector.TrackEvent("enemy_killed", null);
            collector.RecordMetric("frame_ms", 16, "ms");
            store.Pump();
            var copies = File.ReadAllLines(CheckpointFiles().Single());
            Assert.AreEqual(2, copies.Length);
            collector.FlushPending();
            store.Pump();
            Assert.AreEqual(0, CheckpointFiles().Length);
            var uploaded = UploadAll(store);
            foreach (var copy in copies)
            {
                var final = uploaded.Single(l => Id(l) == Id(copy));
                Assert.AreEqual(Count(copy), Count(final));
                Assert.IsFalse(Recovered(final));
            }
        }

        // ---- Races: the real worker thread ----

        // Four threads log repeats while windows close every 300 ms and the
        // worker checkpoints every second. Copies of the storage directory
        // taken while it runs (what a kill would leave) never count an
        // occurrence twice once de-duplicated by event id (the server's rule),
        // and never count more than happened; after a clean stop every
        // occurrence is uploaded exactly once and no checkpoint is left.
        [Test]
        public void WorkerThread_ConcurrentRepeatsAndCloses_CountEachOccurrenceOnce()
        {
            _config.CountCheckpointIntervalSeconds = 1f;
            _config.PersistIntervalMs = 20;
            var store = new OfflineStorage(_config, _dir, true);
            store.CheckpointStamp = Stamp;
            _stores.Add(store);
            store.Start();
            var collector = NewExceptionCollector(store);
            long logged = 0;
            bool stop = false;
            var threads = Enumerable.Range(0, 4).Select(t => new System.Threading.Thread(() =>
            {
                int n = 0;
                while (!System.Threading.Volatile.Read(ref stop))
                {
                    collector.HandleLogMessage("APMT t" + t + " " + (n++ % 3) + ": test error", Stack, LogType.Error);
                    System.Threading.Interlocked.Increment(ref logged);
                    if (n % 50 == 0)
                        System.Threading.Thread.Sleep(1);
                }
            })).ToList();
            threads.ForEach(th => th.Start());

            var snapshots = new List<string>();
            var started = DateTime.UtcNow;
            int closes = 0;
            while ((DateTime.UtcNow - started).TotalSeconds < 3.5)
            {
                System.Threading.Thread.Sleep(300);
                if (++closes % 2 == 0)
                    collector.FlushPending();
                else
                {
                    string path = CopyDirectory();
                    snapshots.Add(path + "|" + ReadLong(ref logged).ToString(CultureInfo.InvariantCulture));
                }
            }
            System.Threading.Volatile.Write(ref stop, true);
            threads.ForEach(th => th.Join());
            collector.Stop();
            store.Shutdown(5000);
            _stores.Remove(store);
            Assert.Greater(store.CheckpointWrites, 0);
            Assert.AreEqual(0, CheckpointFiles().Length, "a clean stop leaves no checkpoint");

            collector.GetStats(out long occurrences, out _, out _);
            Assert.AreEqual(System.Threading.Interlocked.Read(ref logged), occurrences);
            var all = UploadAll(NewStore());
            Assert.IsFalse(all.Any(Recovered));
            Assert.AreEqual(all.Count, all.Select(Id).Distinct().Count());
            Assert.AreEqual(occurrences, all.Sum(Count));

            int recoveredSeen = 0;
            foreach (var snap in snapshots)
            {
                var parts = snap.Split('|');
                long loggedThen = long.Parse(parts[1], CultureInfo.InvariantCulture);
                var copy = new OfflineStorage(_config, parts[0], false);
                var lines = UploadAll(copy);
                copy.Shutdown(0);
                Directory.Delete(parts[0], true);
                // The server keeps the first copy of an id.
                var kept = new Dictionary<string, string>();
                foreach (var l in lines)
                {
                    if (!kept.ContainsKey(Id(l)))
                        kept[Id(l)] = l;
                }
                foreach (var g in lines.GroupBy(Id).Where(g => g.Count() > 1))
                    Assert.AreEqual(1, g.Count(l => !Recovered(l)), "two final counts with one id");
                recoveredSeen += lines.Count(Recovered);
                Assert.LessOrEqual(kept.Values.Sum(Count), loggedThen + 4, "never more than happened");
            }
            Assert.Greater(recoveredSeen, 0, "some copy held a checkpoint");
        }

        private static long ReadLong(ref long value) => System.Threading.Interlocked.Read(ref value);

        // Copies the storage directory as a kill would leave it (files that
        // vanish meanwhile are skipped). Returns the copy's path.
        private string CopyDirectory()
        {
            string to = _dir + "_snap" + Guid.NewGuid().ToString("N").Substring(0, 6);
            Directory.CreateDirectory(to);
            if (Directory.Exists(_dir))
            {
                foreach (var f in Directory.GetFiles(_dir))
                {
                    try
                    {
                        File.Copy(f, Path.Combine(to, Path.GetFileName(f)));
                    }
                    catch (IOException)
                    {
                    }
                }
            }
            return to;
        }

        // ---- Cost: the counting paths stay allocation-free (needs Unity) ----

        [Test]
        public void Repeats_AndSamples_DoNotAllocate_WithCheckpointRequests()
        {
            var exc = new ExceptionCollector(new ApmConfig(), null);
            int requests = 0;
            exc.EnqueueForTests = e => { };
            exc.CheckpointRequestedForTests = urgent => { requests++; };
            exc.ClockForTests = _clock;
            exc.ActivateForTests();
            string message = "Pool not found for enemy 12";
            exc.HandleLogMessage(message, Stack, LogType.Error);
            exc.HandleLogMessage(message, Stack, LogType.Error);
            var into = new List<BaseEvent>();
            // Each delegate runs once before it is measured (a first run can
            // allocate for the runtime, not the SDK); the checkpoint before each
            // measured repeat clears the dirty flag, so the measured repeat also
            // takes the path that requests the next checkpoint.
            TestDelegate repeat = () => { exc.HandleLogMessage(message, Stack, LogType.Error); };
            exc.CollectCheckpoint(into, false);
            repeat();
            into.Clear();
            exc.CollectCheckpoint(into, false); // dirty cleared: the next repeat requests again
            Assert.That(repeat, Is.Not.AllocatingGCMemory());
            Assert.That(repeat, Is.Not.AllocatingGCMemory());

            var custom = new CustomCollector(new ApmConfig(), null, new Timeline());
            custom.EnqueueForTests = e => { };
            custom.CheckpointRequestedForTests = urgent => { requests++; };
            custom.ClockForTests = _clock;
            custom.ActivateForTests();
            string name = "frame_ms", unit = "ms";
            custom.RecordMetric(name, 1, unit);
            custom.RecordMetric(name, 2000, unit);
            into.Clear();
            TestDelegate sample = () => { custom.RecordMetric(name, 16.6, unit); };
            custom.CollectCheckpoint(into, false);
            sample();
            into.Clear();
            custom.CollectCheckpoint(into, false);
            Assert.That(sample, Is.Not.AllocatingGCMemory());
            Assert.That(sample, Is.Not.AllocatingGCMemory());
            Assert.Greater(requests, 2);
        }
    }

    /// <summary>
    /// APM-24 through ApmClient (EditMode: needs the Unity runtime): the
    /// collectors are registered with the storage, the copies carry the
    /// common fields, the final events their ids; consent.
    /// </summary>
    [TestFixture]
    public class CountCheckpointApiTests
    {
        private string _dir;

        private static void ResetPrefs()
        {
            foreach (var key in new[] { "ozx_apm_device_id", "ozx_apm_session_id", "ozx_apm_session_start", ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey })
                PlayerPrefs.DeleteKey(key);
            OzxApm.Utils.DeviceInfo.ClearCacheForTests();
        }

        [SetUp]
        public void SetUp()
        {
            ApmClient.ResetForTests();
            ResetPrefs();
            GameContext.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_cpapi_" + Guid.NewGuid().ToString("N"));
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

        private ApmClient Start()
        {
            var config = new ApmConfig
            {
                ServerUrl = "http://127.0.0.1:9",
                AppVersion = "cp-1.0",
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f,
                MaxCustomEventsPerMinute = 1
            };
            ApmClient.StoragePathForTests = _dir;
            ApmClient.Initialize(config);
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        private string[] Checkpoints() => Directory.Exists(_dir)
            ? Directory.GetFiles(_dir).Where(f => f.Contains(".ckpt")).ToArray()
            : new string[0];

        private static string Id(string line) => Regex.Match(line, "\"event_id\":\"([0-9a-f]*)\"").Groups[1].Value;

        [Test]
        public void CountsAreCheckpointed_WithTheCommonFields_AndReplacedByTheWindowsEvents()
        {
            var client = Start();
            for (int i = 0; i < 6; i++)
                ApmClient.TrackEvent("enemy_killed");
            ApmClient.RecordMetric("frame_ms", 16.5);
            client.ExceptionCollectorForTests.HandleLogMessage("Pool not found: bullet_red", "Ozx.Pool:Get()\n", LogType.Error);
            client.ExceptionCollectorForTests.HandleLogMessage("Pool not found: bullet_red", "Ozx.Pool:Get()\n", LogType.Error);
            client.StorageForTests.Pump();

            var files = Checkpoints();
            Assert.AreEqual(2, files.Length, string.Join(",", files));
            var lines = files.SelectMany(File.ReadAllLines).ToList();
            Assert.AreEqual(3, lines.Count, string.Join("\n", lines)); // aggregated events, metric series, exception repeats
            foreach (var line in lines)
            {
                StringAssert.Contains("\"recovered\":true", line);
                StringAssert.Contains("\"app_version\":\"cp-1.0\"", line);
                StringAssert.Contains("\"device_id\":\"" + client.Session.DeviceId + "\"", line);
            }
            Assert.Greater(ApmClient.GetStats().CountCheckpoints, 0);

            ApmClient.Flush();
            client.StorageForTests.Pump();
            Assert.AreEqual(0, Checkpoints().Length);
            string stored = string.Concat(Directory.GetFiles(_dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText));
            foreach (var line in lines)
                StringAssert.Contains("\"event_id\":\"" + Id(line) + "\"", stored);
            StringAssert.DoesNotContain("\"recovered\"", stored);
        }

        [Test]
        public void ConsentDenied_DeletesTheCheckpoints()
        {
            var client = Start();
            for (int i = 0; i < 3; i++)
                ApmClient.TrackEvent("enemy_killed");
            client.StorageForTests.Pump();
            Assert.AreEqual(1, Checkpoints().Length);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            client.StorageForTests.Pump();
            Assert.AreEqual(0, Checkpoints().Length);
        }
    }
}
