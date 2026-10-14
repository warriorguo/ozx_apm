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
    }
}
