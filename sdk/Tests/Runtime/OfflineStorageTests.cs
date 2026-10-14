using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using NUnit.Framework;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;

namespace OzxApm.Tests
{
    /// <summary>
    /// The storage worker is driven synchronously with Pump() so the tests are deterministic.
    /// </summary>
    [TestFixture]
    public class OfflineStorageTests
    {
        private string _dir;
        private ApmConfig _config;
        private readonly List<OfflineStorage> _stores = new List<OfflineStorage>();

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_test_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig
            {
                BatchSize = 5,
                MaxBatchBytes = 64 * 1024,
                FlushIntervalSeconds = 3600f, // seal only when asked or full
                MaxOfflineStorageBytes = 5 * 1024 * 1024,
                EnableCompression = false,
                LogLevel = LogLevel.None
            };
        }

        [TearDown]
        public void TearDown()
        {
            OfflineStorage.BeforeWriteHook = null;
            OfflineStorage.FailThreadStartForTests = false;
            foreach (var store in _stores)
                store.Shutdown(0);
            _stores.Clear();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private OfflineStorage NewStore()
        {
            var store = new OfflineStorage(_config, _dir, false);
            _stores.Add(store);
            return store;
        }

        private static PerfSampleEvent Perf(float fps)
        {
            return new PerfSampleEvent
            {
                fps = fps,
                event_id = Guid.NewGuid().ToString("N"),
                timestamp = 1700000000000,
                app_version = "1.0.0",
                platform = "Android",
                session_id = "s",
                device_id = "d"
            };
        }

        private string[] SegmentFiles()
        {
            if (!Directory.Exists(_dir))
                return new string[0];
            return Directory.GetFiles(_dir)
                .Where(f => f.EndsWith(".seg", StringComparison.Ordinal) || f.EndsWith(".open", StringComparison.Ordinal))
                .OrderBy(f => f, StringComparer.Ordinal)
                .ToArray();
        }

        private static UploadBatch TakeUpload(OfflineStorage store)
        {
            store.RequestUpload();
            store.Pump();
            var state = store.PollUpload(out var batch);
            return state == UploadPoll.Ready ? batch : null;
        }

        private static string Body(UploadBatch batch)
        {
            if (!batch.Compressed)
                return Encoding.UTF8.GetString(batch.Payload);
            using (var input = new GZipStream(new MemoryStream(batch.Payload), CompressionMode.Decompress))
            using (var reader = new StreamReader(input, Encoding.UTF8))
                return reader.ReadToEnd();
        }

        private static List<long> Seqs(string text)
        {
            return Regex.Matches(text, "\"seq\":(\\d+)")
                .Cast<Match>()
                .Select(m => long.Parse(m.Groups[1].Value, CultureInfo.InvariantCulture))
                .ToList();
        }

        [Test]
        public void Enqueue_PersistsEventsBeforeAnyUpload()
        {
            var store = NewStore();
            var a = Perf(60);
            var b = Perf(30);

            store.Enqueue(a);
            store.Enqueue(b);
            store.Pump();

            var files = SegmentFiles();
            Assert.AreEqual(1, files.Length, "events are on disk right after the worker runs");
            var lines = File.ReadAllLines(files[0]);
            Assert.AreEqual(2, lines.Length);
            StringAssert.Contains(a.event_id, lines[0]);
            StringAssert.Contains(b.event_id, lines[1]);
            StringAssert.Contains("\"seq\":", lines[0]);
        }

        [Test]
        public void Enqueue_DoesNoIoOnTheCallingThread()
        {
            var store = NewStore();
            store.Enqueue(Perf(60));

            Assert.IsFalse(Directory.Exists(_dir), "nothing touches the disk until the worker runs");
        }

        [Test]
        public void Seq_IsMonotonic_AcrossRestarts()
        {
            var first = NewStore();
            for (int i = 0; i < 3; i++) first.Enqueue(Perf(i));
            first.Pump();
            first.Shutdown(0);

            var second = NewStore();
            for (int i = 0; i < 3; i++) second.Enqueue(Perf(i));
            second.Pump();

            var seqs = SegmentFiles().SelectMany(f => Seqs(File.ReadAllText(f))).ToList();
            Assert.AreEqual(6, seqs.Count);
            for (int i = 1; i < seqs.Count; i++)
                Assert.Greater(seqs[i], seqs[i - 1], "seq must increase across launches");
        }

        [Test]
        public void Upload_DeletesSegmentOnlyAfterServerConfirms()
        {
            var store = NewStore();
            var a = Perf(60);
            store.Enqueue(a);
            store.Flush();
            store.Pump();

            var batch = TakeUpload(store);
            Assert.IsNotNull(batch);
            string body = Body(batch);
            StringAssert.StartsWith("{\"events\":[{", body);
            StringAssert.EndsWith("}]}", body);
            StringAssert.Contains(a.event_id, body);
            Assert.AreEqual(1, batch.EventCount);

            // Upload failed: the batch stays on disk.
            store.CompleteUpload(batch, false);
            store.Pump();
            Assert.AreEqual(1, SegmentFiles().Length);

            // Server confirmed: now it is deleted.
            var retry = TakeUpload(store);
            Assert.IsNotNull(retry);
            StringAssert.Contains(a.event_id, Body(retry));
            store.CompleteUpload(retry, true);
            store.Pump();
            Assert.AreEqual(0, SegmentFiles().Length);
            Assert.IsNull(TakeUpload(store));
        }

        [Test]
        public void RepeatedFailures_NeverDiscardEvents()
        {
            var store = NewStore();
            var a = Perf(60);
            store.Enqueue(a);
            store.Flush();
            store.Pump();

            for (int attempt = 0; attempt < 50; attempt++)
            {
                var batch = TakeUpload(store);
                Assert.IsNotNull(batch, "attempt " + attempt);
                StringAssert.Contains(a.event_id, Body(batch));
                store.CompleteUpload(batch, false);
                store.Pump();
            }

            Assert.AreEqual(1, SegmentFiles().Length);
        }

        [Test]
        public void OnlyOneBatchIsInFlight()
        {
            _config.BatchSize = 1;
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Enqueue(Perf(2));
            store.Pump();

            var first = TakeUpload(store);
            Assert.IsNotNull(first);
            Assert.IsNull(TakeUpload(store), "second batch waits until the first one completes");

            store.CompleteUpload(first, true);
            store.Pump();
            Assert.IsNotNull(TakeUpload(store));
        }

        [Test]
        public void Segments_SealAtBatchSize()
        {
            var store = NewStore();
            for (int i = 0; i < 12; i++) store.Enqueue(Perf(i));
            store.Pump();

            var files = SegmentFiles();
            Assert.AreEqual(3, files.Length);
            Assert.AreEqual(2, files.Count(f => f.EndsWith(".seg", StringComparison.Ordinal)));
            Assert.AreEqual(1, files.Count(f => f.EndsWith(".open", StringComparison.Ordinal)));
        }

        [Test]
        public void Upload_IsOldestFirst()
        {
            _config.BatchSize = 1;
            var store = NewStore();
            var first = Perf(1);
            var second = Perf(2);
            store.Enqueue(first);
            store.Enqueue(second);
            store.Pump();

            var batch = TakeUpload(store);
            StringAssert.Contains(first.event_id, Body(batch));
        }

        [Test]
        public void OpenSegmentLeftByACrash_IsUploadedOnNextLaunch()
        {
            var crashed = NewStore();
            var a = Perf(60);
            crashed.Enqueue(a);
            crashed.Pump(); // written to a .open segment, then the process "dies"
            Assert.IsTrue(SegmentFiles()[0].EndsWith(".open", StringComparison.Ordinal));
            crashed.SimulateCrashForTests();

            var next = NewStore();
            next.Pump();
            var batch = TakeUpload(next);
            Assert.IsNotNull(batch);
            StringAssert.Contains(a.event_id, Body(batch));
        }

        [Test]
        public void TornLastLine_IsSkipped()
        {
            Directory.CreateDirectory(_dir);
            string good = "{\"type\":\"perf_sample\",\"event_id\":\"good\",\"seq\":1,\"fps\":60.00}";
            string torn = "{\"type\":\"perf_sample\",\"event_id\":\"torn\",\"se";
            File.WriteAllText(Path.Combine(_dir, "1700000000000-000000.open"), good + "\n" + torn);

            var store = NewStore();
            var batch = TakeUpload(store);

            Assert.IsNotNull(batch);
            Assert.AreEqual(1, batch.EventCount);
            Assert.AreEqual("{\"events\":[" + good + "]}", Body(batch));
        }

        [Test]
        public void SegmentWithNoCompleteEvent_IsDropped()
        {
            Directory.CreateDirectory(_dir);
            File.WriteAllText(Path.Combine(_dir, "1700000000000-000000.seg"), "{\"type\":\"perf");

            var store = NewStore();
            Assert.IsNull(TakeUpload(store));
            Assert.AreEqual(0, SegmentFiles().Length);
        }

        [Test]
        public void LegacyOfflineFile_IsUploadedUnchanged()
        {
            Directory.CreateDirectory(_dir);
            string legacy = "{\"events\":[{\"type\":\"jank\",\"timestamp\":1},{\"type\":\"jank\",\"timestamp\":2}]}";
            File.WriteAllText(Path.Combine(_dir, "ozx_apm_offline_1700000000000.json"), legacy);

            var store = NewStore();
            var batch = TakeUpload(store);

            Assert.IsNotNull(batch);
            Assert.AreEqual(legacy, Body(batch));
            Assert.AreEqual(2, batch.EventCount);

            store.CompleteUpload(batch, true);
            store.Pump();
            Assert.IsFalse(File.Exists(Path.Combine(_dir, "ozx_apm_offline_1700000000000.json")));
        }

        [Test]
        public void StorageCap_DropsOldestEvents()
        {
            _config.BatchSize = 1; // one event per segment
            _config.MaxOfflineStorageBytes = 4096;
            var store = NewStore();
            var events = new List<PerfSampleEvent>();
            for (int i = 0; i < 60; i++)
            {
                var e = Perf(i);
                events.Add(e);
                store.Enqueue(e);
            }
            store.Pump();

            long total = SegmentFiles().Sum(f => new FileInfo(f).Length);
            Assert.LessOrEqual(total, 4096);
            Assert.AreEqual(total, store.GetStorageSize());
            Assert.Greater(store.DroppedEventCount, 0);

            string all = string.Concat(SegmentFiles().Select(File.ReadAllText));
            StringAssert.Contains(events[events.Count - 1].event_id, all, "newest events are kept");
            StringAssert.DoesNotContain(events[0].event_id, all, "oldest events are dropped");
        }

        [Test]
        public void ConsentDenied_WritesNothing()
        {
            _config.Consent = ConsentState.Denied;
            var store = NewStore();
            store.Enqueue(Perf(60));
            store.Flush();
            store.Pump();

            Assert.IsFalse(Directory.Exists(_dir), "no file and no directory without consent");
        }

        [Test]
        public void ConsentUnknown_WritesNothing()
        {
            _config.Consent = ConsentState.Unknown;
            var store = NewStore();
            store.Enqueue(Perf(60));
            store.Pump();

            Assert.IsFalse(Directory.Exists(_dir));
        }

        [Test]
        public void ConsentRevokedAfterEnqueue_IsCheckedBeforeWriting()
        {
            var store = NewStore();
            store.Enqueue(Perf(60));
            _config.Consent = ConsentState.Denied;
            store.Pump();

            Assert.IsFalse(Directory.Exists(_dir));
        }

        [Test]
        public void ConsentNotGranted_DoesNotUpload()
        {
            var store = NewStore();
            store.Enqueue(Perf(60));
            store.Flush();
            store.Pump();

            _config.Consent = ConsentState.Unknown;
            Assert.IsNull(TakeUpload(store));
        }

        [Test]
        public void Clear_DeletesAllStoredEvents()
        {
            var store = NewStore();
            for (int i = 0; i < 7; i++) store.Enqueue(Perf(i));
            store.Pump();
            Assert.Greater(SegmentFiles().Length, 0);

            store.Clear();
            store.Pump();

            Assert.AreEqual(0, SegmentFiles().Length);
            Assert.AreEqual(0, store.GetStorageSize());
        }

        [Test]
        public void MemoryOnlyMode_TouchesNoFiles_ButStillUploads()
        {
            _config.EnableOfflineStorage = false;
            var store = NewStore();
            var a = Perf(60);
            store.Enqueue(a);
            store.Flush();
            store.Pump();

            Assert.IsFalse(Directory.Exists(_dir));
            var batch = TakeUpload(store);
            Assert.IsNotNull(batch);
            StringAssert.Contains(a.event_id, Body(batch));
        }

        [Test]
        public void Compression_ProducesValidGzip()
        {
            _config.EnableCompression = true;
            _config.BatchSize = 20;
            var store = NewStore();
            for (int i = 0; i < 20; i++) store.Enqueue(Perf(i));
            store.Pump();

            var batch = TakeUpload(store);
            Assert.IsNotNull(batch);
            Assert.IsTrue(batch.Compressed);
            string body = Body(batch);
            Assert.AreEqual(20, Seqs(body).Count);
        }

        [Test]
        public void StoredEvents_AreCultureIndependent()
        {
            var saved = Thread.CurrentThread.CurrentCulture;
            try
            {
                Thread.CurrentThread.CurrentCulture = new CultureInfo("de-DE");
                var store = NewStore();
                store.Enqueue(Perf(59.5f));
                store.Flush();
                store.Pump();
                string body = Body(TakeUpload(store));

                StringAssert.Contains("\"fps\":59.50", body);
            }
            finally
            {
                Thread.CurrentThread.CurrentCulture = saved;
            }
        }

        [Test]
        public void WorkerThread_PersistsWithoutPumping()
        {
            var store = new OfflineStorage(_config, _dir, true);
            _stores.Add(store);
            store.Start();

            store.Enqueue(Perf(60));
            store.Flush();
            Assert.IsTrue(store.WaitIdle(5000));

            Assert.AreEqual(1, SegmentFiles().Length);
            store.Shutdown(5000);
        }

        // ---------------------------------------------------------------
        // Write failures (disk full, I/O errors)
        // ---------------------------------------------------------------

        private static List<string> EventIds(string text)
        {
            return Regex.Matches(text, "\"event_id\":\"([0-9a-f]{32})\"").Cast<Match>().Select(m => m.Groups[1].Value).ToList();
        }

        private static void AssertEveryLineIsValidJson(string file)
        {
            var bytes = File.ReadAllBytes(file);
            int start = 0;
            for (int i = 0; i < bytes.Length; i++)
            {
                if (bytes[i] != (byte)'\n') continue;
                string line = Encoding.UTF8.GetString(bytes, start, i - start);
                Assert.IsTrue(line.StartsWith("{") && line.EndsWith("}") && line.IndexOf("}{", StringComparison.Ordinal) < 0,
                    "corrupt line in " + Path.GetFileName(file) + ": " + line);
                start = i + 1;
            }
            Assert.AreEqual(bytes.Length, start, "file ends with an incomplete line: " + Path.GetFileName(file));
        }

        [Test]
        public void PartialWriteThenFailure_IsTruncated_AndEventsAreRetried()
        {
            var store = NewStore();
            var first = Perf(1);
            store.Enqueue(first);
            store.Pump(); // one good line on disk

            // Disk fills up mid-write: half a line reaches the file, then the write throws.
            OfflineStorage.BeforeWriteHook = stream =>
            {
                var partial = Encoding.UTF8.GetBytes("{\"type\":\"perf_sample\",\"event_id\":\"to");
                stream.Write(partial, 0, partial.Length);
                stream.Flush();
                throw new IOException("Disk full");
            };
            var a = Perf(2);
            var b = Perf(3);
            store.Enqueue(a);
            store.Enqueue(b);
            store.Pump();

            foreach (var f in SegmentFiles())
                AssertEveryLineIsValidJson(f);
            string onDisk = string.Concat(SegmentFiles().Select(File.ReadAllText));
            CollectionAssert.AreEquivalent(new[] { first.event_id }, EventIds(onDisk));

            // Space is back.
            OfflineStorage.BeforeWriteHook = null;
            store.ResetWriteBackoffForTests();
            store.Pump();

            foreach (var f in SegmentFiles())
                AssertEveryLineIsValidJson(f);
            onDisk = string.Concat(SegmentFiles().Select(File.ReadAllText));
            CollectionAssert.AreEquivalent(new[] { first.event_id, a.event_id, b.event_id }, EventIds(onDisk));
            var seqs = Seqs(onDisk);
            Assert.AreEqual(seqs.Count, seqs.Distinct().Count(), "retried events keep their seq");
        }

        [Test]
        public void DiskFull_KeepsEventsInMemory_AndRateLimitsWarnings()
        {
            var store = NewStore();
            OfflineStorage.BeforeWriteHook = _ => throw new IOException("Disk full");

            var ids = new List<string>();
            for (int round = 0; round < 20; round++)
            {
                var e = Perf(round);
                ids.Add(e.event_id);
                store.Enqueue(e);
                store.ResetWriteBackoffForTests();
                store.Pump();
            }
            Assert.LessOrEqual(store.WarningsLogged, 1, "a full disk must not flood the log");
            Assert.AreEqual(0, EventIds(string.Concat(SegmentFiles().Select(File.ReadAllText))).Count);

            OfflineStorage.BeforeWriteHook = null;
            store.ResetWriteBackoffForTests();
            store.Pump();
            CollectionAssert.AreEquivalent(ids, EventIds(string.Concat(SegmentFiles().Select(File.ReadAllText))));
        }

        [Test]
        public void TornLineFollowedByMoreData_IsDropped_BodyStaysValid()
        {
            Directory.CreateDirectory(_dir);
            string good = "{\"type\":\"perf_sample\",\"event_id\":\"g1\",\"fps\":1.00}";
            string merged = "{\"type\":\"perf_sample\",\"event_id\":\"torn\",\"se" + "{\"type\":\"perf_sample\",\"event_id\":\"g2\",\"fps\":2.00}";
            string good3 = "{\"type\":\"jank\",\"event_id\":\"g3\",\"recent_events\":[\"a}{b\"]}";
            File.WriteAllText(Path.Combine(_dir, "1700000000000-000000.seg"), good + "\n" + merged + "\n" + good3 + "\n");

            var store = NewStore();
            var batch = TakeUpload(store);

            Assert.IsNotNull(batch);
            Assert.AreEqual(2, batch.EventCount);
            Assert.AreEqual("{\"events\":[" + good + "," + good3 + "]}", Body(batch));
        }

        // ---------------------------------------------------------------
        // Sequence numbers
        // ---------------------------------------------------------------

        [Test]
        public void Seq_StaysMonotonic_WhenSeqFileIsLost()
        {
            var first = NewStore();
            for (int i = 0; i < 3; i++) first.Enqueue(Perf(i));
            first.Pump();
            first.Shutdown(0);
            long maxBefore = SegmentFiles().SelectMany(f => Seqs(File.ReadAllText(f))).Max();

            File.Delete(Path.Combine(_dir, "seq"));

            var second = NewStore();
            second.Enqueue(Perf(9));
            second.Pump();
            long newest = SegmentFiles().SelectMany(f => Seqs(File.ReadAllText(f))).Max();
            Assert.Greater(newest, maxBefore);
            Assert.AreEqual(4, SegmentFiles().SelectMany(f => Seqs(File.ReadAllText(f))).Distinct().Count());
        }

        // ---------------------------------------------------------------
        // Cap
        // ---------------------------------------------------------------

        [Test]
        public void Cap_NeverEvictsTheBatchInFlight_AndAccountingMatchesDisk()
        {
            _config.BatchSize = 1;
            _config.MaxOfflineStorageBytes = 4096;
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Pump();
            var inFlight = TakeUpload(store);

            for (int i = 2; i < 100; i++) store.Enqueue(Perf(i));
            store.Pump();

            Assert.IsTrue(File.Exists(Path.Combine(_dir, inFlight.SegmentName)), "in-flight batch was evicted");
            long actual = SegmentFiles().Sum(f => new FileInfo(f).Length);
            Assert.AreEqual(actual, store.GetStorageSize());
            Assert.LessOrEqual(actual, 4096);

            store.CompleteUpload(inFlight, false);
            store.Pump();
            Assert.AreEqual(SegmentFiles().Sum(f => new FileInfo(f).Length), store.GetStorageSize());
        }

        [Test]
        public void Cap_EvictsPerfSamplesBeforeExceptions()
        {
            _config.BatchSize = 1;
            _config.MaxOfflineStorageBytes = 4096;
            var store = NewStore();
            var exc = new ExceptionEvent
            {
                event_id = Guid.NewGuid().ToString("N"), timestamp = 1, app_version = "1", platform = "Android",
                session_id = "s", device_id = "d", fingerprint = "f", message = "boom", count = 1
            };
            store.Enqueue(exc); // oldest
            for (int i = 0; i < 80; i++) store.Enqueue(Perf(i));
            store.Pump();

            string all = string.Concat(SegmentFiles().Select(File.ReadAllText));
            StringAssert.Contains(exc.event_id, all, "the exception survives while perf samples are evicted");
            Assert.Greater(store.DroppedEventCount, 0);
        }

        [Test]
        public void Cap_CountsDroppedEventsOfSegmentsFromEarlierLaunches()
        {
            Directory.CreateDirectory(_dir);
            string line = "{\"type\":\"perf_sample\",\"event_id\":\"old\",\"fps\":1.00,\"pad\":\"" + new string('x', 900) + "\"}\n";
            File.WriteAllText(Path.Combine(_dir, "1600000000000-000000.seg"), line + line + line);
            _config.MaxOfflineStorageBytes = 4096;
            _config.BatchSize = 1;
            var store = NewStore();
            for (int i = 0; i < 10; i++) store.Enqueue(Perf(i));
            store.Pump();

            Assert.IsFalse(File.Exists(Path.Combine(_dir, "1600000000000-000000.seg")));
            Assert.GreaterOrEqual(store.DroppedEventCount, 3);
        }

        [Test]
        public void MemoryOnlyMode_StaysUnderOneMegabyte()
        {
            _config.EnableOfflineStorage = false;
            _config.MaxOfflineStorageBytes = 50 * 1024 * 1024;
            _config.MaxQueueSize = 100000;
            var store = NewStore();
            for (int i = 0; i < 20000; i++) store.Enqueue(Perf(i));
            store.Pump();

            Assert.LessOrEqual(store.GetStorageSize(), OfflineStorage.MemoryOnlyMaxBytes);
            Assert.IsFalse(Directory.Exists(_dir));
        }

        // ---------------------------------------------------------------
        // Upload batching, quarantine, legacy files
        // ---------------------------------------------------------------

        [Test]
        public void ConsecutiveSmallSegments_AreMergedIntoOneUpload_AndAllDeleted()
        {
            _config.BatchSize = 20;
            var store = NewStore();
            var ids = new List<string>();
            for (int s = 0; s < 3; s++)
            {
                for (int i = 0; i < 2; i++)
                {
                    var e = Perf(i);
                    ids.Add(e.event_id);
                    store.Enqueue(e);
                }
                store.Flush();
                store.Pump(); // seals a 2-event segment
            }
            Assert.AreEqual(3, SegmentFiles().Length);

            var batch = TakeUpload(store);
            Assert.AreEqual(6, batch.EventCount);
            CollectionAssert.AreEquivalent(ids, EventIds(Body(batch)));

            store.CompleteUpload(batch, UploadOutcome.Delivered);
            store.Pump();
            Assert.AreEqual(0, SegmentFiles().Length);
        }

        [Test]
        public void MergedUpload_RespectsBatchSize()
        {
            _config.BatchSize = 5;
            var store = NewStore();
            for (int s = 0; s < 3; s++)
            {
                for (int i = 0; i < 2; i++) store.Enqueue(Perf(i));
                store.Flush();
                store.Pump();
            }
            var batch = TakeUpload(store);
            Assert.AreEqual(4, batch.EventCount, "2+2 fit, the third segment would exceed 5");
        }

        [Test]
        public void RejectedBatch_IsQuarantined_NotUploadedAgain()
        {
            var store = NewStore();
            var e = Perf(1);
            store.Enqueue(e);
            store.Flush();
            store.Pump();

            var batch = TakeUpload(store);
            store.CompleteUpload(batch, UploadOutcome.Rejected);
            store.Pump();

            Assert.AreEqual(0, SegmentFiles().Length);
            Assert.IsNull(TakeUpload(store));
            var quarantined = Directory.GetFiles(Path.Combine(_dir, "quarantine"));
            Assert.AreEqual(1, quarantined.Length);
            StringAssert.Contains(e.event_id, File.ReadAllText(quarantined[0]));
            Assert.AreEqual(0, store.GetStorageSize());
        }

        [Test]
        public void Quarantine_IsCapped()
        {
            Directory.CreateDirectory(Path.Combine(_dir, "quarantine"));
            for (int i = 0; i < 10; i++)
                File.WriteAllText(Path.Combine(_dir, "quarantine", "1600000000000-00000" + i + ".seg"), new string('x', 100 * 1024));
            var old = Path.Combine(_dir, "quarantine", "1500000000000-000000.seg");
            File.WriteAllText(old, "x");
            File.SetLastWriteTimeUtc(old, DateTime.UtcNow.AddDays(-8));

            var store = NewStore();
            store.Pump();

            var files = Directory.GetFiles(Path.Combine(_dir, "quarantine"));
            Assert.LessOrEqual(files.Sum(f => new FileInfo(f).Length), OfflineStorage.MaxQuarantineBytes);
            Assert.IsFalse(File.Exists(old), "expired quarantine files are removed");
            Assert.IsTrue(File.Exists(Path.Combine(_dir, "quarantine", "1600000000000-000009.seg")), "newest kept");
        }

        [Test]
        public void LegacyFilesAndNewSegments_AreUploadedSeparately_LegacyFirst()
        {
            Directory.CreateDirectory(_dir);
            string legacy = "{\"events\":[{\"type\":\"jank\",\"timestamp\":1}]}";
            File.WriteAllText(Path.Combine(_dir, "ozx_apm_offline_1700000000000.json"), legacy);
            _config.BatchSize = 20;
            var store = NewStore();
            for (int s = 0; s < 2; s++)
            {
                store.Enqueue(Perf(s));
                store.Flush();
                store.Pump();
            }

            var first = TakeUpload(store);
            Assert.AreEqual(legacy, Body(first), "legacy body is sent unchanged and alone");
            store.CompleteUpload(first, UploadOutcome.Delivered);
            store.Pump();

            var second = TakeUpload(store);
            Assert.AreEqual(2, second.EventCount, "new segments merged");
            StringAssert.StartsWith("{\"events\":[{\"type\":\"perf_sample\"", Body(second));
        }

        // ---------------------------------------------------------------
        // Lifecycle
        // ---------------------------------------------------------------

        [Test]
        public void SecondInstanceOnSameDirectory_FallsBackToMemory()
        {
            var first = NewStore();
            first.Pump();
            var second = NewStore();
            var e = Perf(1);
            second.Enqueue(e);
            second.Pump();

            Assert.IsTrue(first.PersistsToDisk);
            Assert.IsFalse(second.PersistsToDisk, "two writers on one directory would corrupt it");
            StringAssert.DoesNotContain(e.event_id, string.Concat(SegmentFiles().Select(File.ReadAllText)));

            // Once the first is gone, a new instance owns the directory again.
            first.Shutdown(0);
            var third = NewStore();
            third.Pump();
            Assert.IsTrue(third.PersistsToDisk);
        }

        [Test]
        public void ThreadStartFailure_FallsBackToTick()
        {
            OfflineStorage.FailThreadStartForTests = true;
            _config.PersistIntervalMs = 0;
            var store = new OfflineStorage(_config, _dir, true);
            _stores.Add(store);
            store.Start();
            Assert.IsFalse(store.HasWorkerThread);

            store.Enqueue(Perf(1));
            store.Tick();
            Assert.AreEqual(1, SegmentFiles().Length);
        }

        [Test]
        public void ConsentWithdrawnMidUpload_CompletesButStartsNothing()
        {
            _config.BatchSize = 1;
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Enqueue(Perf(2));
            store.Pump();
            var batch = TakeUpload(store);

            _config.Consent = ConsentState.Denied;
            store.CompleteUpload(batch, UploadOutcome.Delivered);
            store.Pump();
            Assert.AreEqual(1, SegmentFiles().Length);
            Assert.IsNull(TakeUpload(store));
        }

        [Test]
        public void ShutdownWithBatchInFlight_KeepsItForNextLaunch()
        {
            var store = NewStore();
            var e = Perf(1);
            store.Enqueue(e);
            store.Flush();
            store.Pump();
            Assert.IsNotNull(TakeUpload(store)); // never completed
            store.Shutdown(0);

            var next = NewStore();
            var batch = TakeUpload(next);
            Assert.IsNotNull(batch);
            StringAssert.Contains(e.event_id, Body(batch));
        }

        [Test]
        public void IdleWorker_DoesNotSpin()
        {
            var store = new OfflineStorage(_config, _dir, true);
            _stores.Add(store);
            store.Start();
            store.Enqueue(Perf(1));
            store.Flush();
            Assert.IsTrue(store.WaitIdle(5000));
            Assert.AreEqual(1, SegmentFiles().Length, "written right away, without waiting for a timer");
        }
    }
}
