using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools;
using UnityEngine.TestTools.Constraints;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using OzxApm.Utils;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-14: what the storage does when consent is withdrawn or the device id
    /// is reset (OfflineStorage.Clear), driven synchronously with Pump().
    /// </summary>
    [TestFixture]
    public class OfflineStorageConsentTests
    {
        private string _dir;
        private ApmConfig _config;
        private readonly List<OfflineStorage> _stores = new List<OfflineStorage>();

        [SetUp]
        public void SetUp()
        {
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_consent_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig
            {
                BatchSize = 5,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
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

        private OfflineStorage NewStore(bool thread = false)
        {
            var store = new OfflineStorage(_config, _dir, thread);
            _stores.Add(store);
            if (thread)
                store.Start();
            return store;
        }

        private static PerfSampleEvent Perf(float fps)
        {
            return new PerfSampleEvent { fps = fps, timestamp = 1700000000000, app_version = "1", platform = "Android", session_id = "s", device_id = "d" };
        }

        private string[] AllFiles()
        {
            return Directory.Exists(_dir) ? Directory.GetFiles(_dir, "*", SearchOption.AllDirectories) : new string[0];
        }

        private string AllText()
        {
            return string.Concat(AllFiles().Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText));
        }

        private static UploadBatch TakeUpload(OfflineStorage store)
        {
            store.RequestUpload();
            store.Pump();
            return store.PollUpload(out var batch) == UploadPoll.Ready ? batch : null;
        }

        [Test]
        public void Clear_DeletesSegmentsQuarantineSequenceFileAndDirectory()
        {
            var store = NewStore();
            for (int i = 0; i < 12; i++)
                store.Enqueue(Perf(i));
            store.Pump();
            Directory.CreateDirectory(Path.Combine(_dir, "quarantine"));
            File.WriteAllText(Path.Combine(_dir, "quarantine", "1600000000000-000000.seg"), "{}\n");
            Assert.That(AllFiles().Any(f => Path.GetFileName(f) == "seq"));
            Assert.Greater(AllFiles().Length, 3);

            store.Clear();
            store.Pump();

            Assert.IsFalse(Directory.Exists(_dir), "no file and no directory left: " + string.Join(",", AllFiles()));
            Assert.AreEqual(0, store.GetStorageSize());
            Assert.IsNull(TakeUpload(store));
        }

        [Test]
        public void Clear_DropsQueuedEventsAtOnce()
        {
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Enqueue(Perf(2));
            store.Clear();
            Assert.AreEqual(0, store.PendingEventCount);
            store.Pump();
            Assert.IsFalse(Directory.Exists(_dir));
        }

        [Test]
        public void Clear_KeepsTheSequence_UnlessReset()
        {
            var store = NewStore();
            for (int i = 0; i < 3; i++)
                store.Enqueue(Perf(i));
            store.Pump();
            store.Clear();
            store.Pump();
            store.Enqueue(Perf(4));
            store.Pump();
            StringAssert.Contains("\"seq\":4,", AllText(), "same process, same identity: seq goes on");
            Assert.That(AllFiles().Any(f => Path.GetFileName(f) == "seq"), "the reservation is written again before use");

            store.Clear(true);
            store.Pump();
            store.Enqueue(Perf(5));
            store.Pump();
            StringAssert.Contains("\"seq\":1,", AllText(), "new device id: seq starts again");
            Assert.AreEqual(1, store.LastSeq);
        }

        [Test]
        public void Clear_VoidsABatchPreparedBeforeIt()
        {
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Flush();
            store.Pump();
            store.RequestUpload();
            store.Pump(); // the worker prepared a batch ...

            store.Clear(); // ... and consent is withdrawn before the uploader polls it
            Assert.AreEqual(UploadPoll.Empty, store.PollUpload(out var batch));
            Assert.IsNull(batch);
            store.Pump();
            Assert.IsFalse(Directory.Exists(_dir));
        }

        [Test]
        public void Clear_WithAnUploadRequested_OnlyLaterEventsAreUploaded()
        {
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Flush();
            store.Pump();
            store.RequestUpload();
            store.Clear();
            var fresh = Perf(2);
            fresh.event_id = "fresh-event";
            store.Enqueue(fresh);
            store.Flush();
            store.Pump(); // Clear, then write and seal the new event, then prepare
            Assert.AreEqual(UploadPoll.Ready, store.PollUpload(out var batch));
            string body = System.Text.Encoding.UTF8.GetString(batch.Payload);
            StringAssert.Contains("fresh-event", body);
            Assert.AreEqual(1, batch.EventCount, "only the event enqueued after Clear");
        }

        /// <summary>
        /// The exact interleaving: the worker has taken its work (no Clear yet),
        /// Clear runs, the worker then hands out a batch of the deleted data and
        /// the uploader polls before the worker's next iteration deletes it.
        /// </summary>
        [Test]
        public void ClearBetweenTheWorkersPreparationAndThePoll_BatchIsNotHandedOut()
        {
            var store = NewStore(thread: true);
            store.Enqueue(Perf(1));
            store.Flush();
            Assert.IsTrue(store.WaitIdle(5000));

            using (var prepared = new ManualResetEventSlim(false))
            using (var polled = new ManualResetEventSlim(false))
            {
                store.PrepareUploadHookForTests = after =>
                {
                    if (!after)
                    {
                        store.Clear(); // consent withdrawn right now, on another thread
                        return;
                    }
                    store.PrepareUploadHookForTests = null;
                    prepared.Set();
                    polled.Wait(5000); // hold the worker until the uploader has polled
                };
                store.RequestUpload();
                Assert.IsTrue(prepared.Wait(5000), "worker prepared an upload");
                var poll = store.PollUpload(out var batch);
                polled.Set();
                Assert.AreNotEqual(UploadPoll.Ready, poll, "a batch built before Clear must not be handed out");
                Assert.IsNull(batch);
            }
            Assert.IsTrue(store.WaitIdle(5000));
            Assert.IsFalse(Directory.Exists(_dir));
        }

        /// <summary>
        /// The worker prepares uploads on its own thread while the main thread
        /// clears (device id reset: consent stays granted). No batch handed out
        /// after Clear returns may contain an event enqueued before it.
        /// </summary>
        [Test]
        public void ClearRacingUploadPreparation_WithWorkerThread_NoOldEventAfterClear()
        {
            _config.PersistIntervalMs = 1;
            _config.BatchSize = 2;
            for (int round = 0; round < 30; round++)
            {
                var store = NewStore(thread: true);
                for (int i = 0; i < 40; i++)
                {
                    var e = Perf(i);
                    e.session_id = "old-session";
                    store.Enqueue(e);
                    if (i % 5 == 0)
                    {
                        // Uploads under way, as the reporter does.
                        store.RequestUpload();
                        if (store.PollUpload(out var b) == UploadPoll.Ready)
                            store.CompleteUpload(b, UploadOutcome.Retry);
                    }
                }
                store.Flush();
                Thread.Sleep(round % 4);
                store.RequestUpload(); // the worker may be preparing right now
                store.Clear(true);

                var fresh = Perf(1);
                fresh.session_id = "new-session";
                store.Enqueue(fresh);
                store.Flush();
                int seen = 0;
                var deadline = DateTime.UtcNow.AddSeconds(5);
                while (seen == 0 && DateTime.UtcNow < deadline)
                {
                    store.RequestUpload();
                    store.WaitIdle(1000);
                    if (store.PollUpload(out var batch) != UploadPoll.Ready)
                        continue;
                    string body = System.Text.Encoding.UTF8.GetString(batch.Payload);
                    StringAssert.DoesNotContain("old-session", body, "round " + round);
                    seen += batch.EventCount;
                    store.CompleteUpload(batch, UploadOutcome.Delivered);
                }
                Assert.AreEqual(1, seen, "round " + round + ": the new event is uploaded");
                store.Shutdown(1000);
                _stores.Clear();
                if (Directory.Exists(_dir))
                    Directory.Delete(_dir, true);
            }
        }

        [Test]
        public void Clear_WhileABatchIsBeingUploaded_ItsCompletionIsHarmless()
        {
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Flush();
            store.Pump();
            var inFlight = TakeUpload(store);
            Assert.IsNotNull(inFlight);

            store.Clear();
            store.Pump();
            store.CompleteUpload(inFlight, UploadOutcome.Delivered);
            store.Pump();
            Assert.IsFalse(Directory.Exists(_dir));

            store.Enqueue(Perf(2));
            store.Flush();
            store.Pump();
            var next = TakeUpload(store);
            Assert.IsNotNull(next, "uploads work again after a Clear");
            Assert.AreEqual(1, next.EventCount);
        }

        [Test]
        public void Undecided_WritesNothing_NotEvenTheSequenceFile()
        {
            _config.Consent = ConsentState.Undecided;
            var store = NewStore();
            store.Enqueue(Perf(1));
            store.Flush();
            store.Pump();
            Assert.IsFalse(Directory.Exists(_dir));

            // With data stored earlier: kept, but nothing is added or rewritten.
            _config.Consent = ConsentState.Granted;
            store.Enqueue(Perf(2));
            store.Pump();
            long before = AllFiles().Sum(f => new FileInfo(f).Length);
            string seq = File.ReadAllText(Path.Combine(_dir, "seq"));
            _config.Consent = ConsentState.Undecided;
            store.Enqueue(Perf(3));
            store.Flush();
            store.Pump();
            Assert.AreEqual(before, AllFiles().Sum(f => new FileInfo(f).Length), "nothing added");
            Assert.AreEqual(seq, File.ReadAllText(Path.Combine(_dir, "seq")), "sequence reservation not rewritten");
            Assert.IsNull(TakeUpload(store), "nothing uploaded while undecided");
            _config.Consent = ConsentState.Granted;
            Assert.IsNotNull(TakeUpload(store), "uploaded once granted");
        }

        [Test]
        public void DenyWhileAnotherThreadEnqueues_WithWorkerThread_LeavesNothingOnDisk()
        {
            _config.PersistIntervalMs = 1;
            for (int round = 0; round < 20; round++)
            {
                _config.Consent = ConsentState.Granted;
                var store = NewStore(thread: true);
                using (var stop = new ManualResetEventSlim(false))
                {
                    var producer = new Thread(() =>
                    {
                        int i = 0;
                        while (!stop.IsSet)
                            store.Enqueue(Perf(i++));
                    });
                    producer.Start();
                    Thread.Sleep(5 + round % 7); // let the worker write some segments
                    _config.Consent = ConsentState.Denied;
                    store.Clear();
                    Thread.Sleep(round % 3);
                    stop.Set();
                    producer.Join();
                }
                Assert.IsTrue(store.WaitIdle(5000), "round " + round);
                Assert.IsFalse(Directory.Exists(_dir), "round " + round + ": " + string.Join(",", AllFiles()));
                Assert.IsNull(TakeUpload(store));
                store.Shutdown(1000);
                _stores.Clear();
            }
        }
    }

    /// <summary>
    /// APM-14 end to end on ApmClient: consent states and transitions,
    /// persistence, local deletion, device id reset, idle cost when denied.
    /// The storage worker is pumped by the test; uploads are off (Editor).
    /// </summary>
    [TestFixture]
    public class ApmClientConsentTests
    {
        private const string DeviceKey = "ozx_apm_device_id";
        private const string SessionKey = "ozx_apm_session_id";
        private const string SessionStartKey = "ozx_apm_session_start";
        private static readonly string[] Keys = { DeviceKey, SessionKey, SessionStartKey, ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey };

        private string _dir;

        private void ResetAll()
        {
            ApmClient.ResetForTests();
            foreach (var key in Keys)
                PlayerPrefs.DeleteKey(key);
            DeviceInfo.ClearCacheForTests();
        }

        [SetUp]
        public void SetUp()
        {
            ResetAll();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_client_" + Guid.NewGuid().ToString("N"));
        }

        [TearDown]
        public void TearDown()
        {
            ResetAll();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private static ApmConfig NewConfig()
        {
            return new ApmConfig
            {
                ServerUrl = "http://127.0.0.1:9",
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f
            };
        }

        private ApmClient Start(ApmConfig config = null)
        {
            ApmClient.StoragePathForTests = _dir;
            ApmClient.Initialize(config ?? NewConfig());
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        /// <summary>Simulates a kill and a new launch: statics and caches gone, PlayerPrefs and files kept.</summary>
        private ApmClient Relaunch(ApmConfig config = null)
        {
            ApmClient.ResetForTests();
            DeviceInfo.ClearCacheForTests();
            return Start(config);
        }

        private static void Emit(ApmClient client, int n)
        {
            for (int i = 0; i < n; i++)
                client.EnqueueEvent(new ExceptionEvent { fingerprint = "f", message = "m", stack = "s", count = 1 });
        }

        private static void Pump(ApmClient client)
        {
            client.StorageForTests.Pump();
        }

        private bool AnyFiles => Directory.Exists(_dir) && Directory.GetFileSystemEntries(_dir).Length > 0;

        private string StoredText()
        {
            return Directory.Exists(_dir)
                ? string.Concat(Directory.GetFiles(_dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        [Test]
        public void Defaults_TechnicalGranted_AnalyticsUndecided()
        {
            var client = Start();
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual(ConsentState.Undecided, ApmClient.GetConsent(ConsentCategory.Analytics));
            Assert.AreEqual(ConsentState.Granted, ApmClient.Consent);
            var stats = ApmClient.GetStats();
            Assert.IsTrue(stats.Collecting);
            Assert.AreEqual(ConsentState.Undecided, stats.AnalyticsConsent);

            Emit(client, 2);
            Pump(client);
            Assert.IsTrue(AnyFiles);
            Assert.IsFalse(PlayerPrefs.HasKey(ConsentStore.TechnicalKey), "defaults are not stored, only choices");
        }

        [Test]
        public void OptInPreset_CollectsStoresAndUploadsNothing_UntilGranted()
        {
            var client = Start(NewConfig().UseOptInConsent());
            Assert.AreEqual(ConsentState.Undecided, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual(ConsentState.Undecided, ApmClient.GetConsent(ConsentCategory.Analytics));
            Assert.IsFalse(ApmClient.GetStats().Collecting);

            Emit(client, 5);
            ApmClient.Flush();
            for (int i = 0; i < 10; i++)
                client.UpdateForTests();
            Pump(client);
            Assert.AreEqual(0, ApmClient.GetStats().PendingEvents);
            Assert.IsFalse(Directory.Exists(_dir), "no event file, no directory");
            Assert.IsFalse(PlayerPrefs.HasKey(DeviceKey), "no id stored before consent");
            Assert.IsFalse(PlayerPrefs.HasKey(SessionKey));

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            Assert.IsTrue(ApmClient.GetStats().Collecting);
            Emit(client, 1);
            Pump(client);
            Assert.IsTrue(AnyFiles);
            Assert.AreEqual(client.Session.DeviceId, PlayerPrefs.GetString(DeviceKey));
        }

        [Test]
        public void GrantedThenDenied_DeletesEverythingLocal_AndStopsCollecting()
        {
            var client = Start();
            Emit(client, 30);
            Pump(client);
            Assert.IsTrue(AnyFiles);
            Assert.IsTrue(PlayerPrefs.HasKey(DeviceKey));
            Emit(client, 3); // queued, not yet written

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Assert.AreEqual(0, ApmClient.GetStats().PendingEvents, "queued events dropped at once");
            Assert.IsFalse(ApmClient.GetStats().Collecting);
            Pump(client);

            Assert.IsFalse(Directory.Exists(_dir), "every event file and the directory are gone");
            Assert.IsFalse(PlayerPrefs.HasKey(DeviceKey), "stored device id removed");
            Assert.IsFalse(PlayerPrefs.HasKey(SessionKey), "stored session id removed");
            Assert.AreEqual((int)ConsentState.Denied, PlayerPrefs.GetInt(ConsentStore.TechnicalKey));

            Emit(client, 3);
            for (int i = 0; i < 5; i++)
                client.UpdateForTests();
            Pump(client);
            Assert.IsFalse(Directory.Exists(_dir), "nothing collected while denied");
        }

        [Test]
        public void DeniedThenGranted_Resumes_WithTheSameIdsInThisProcess()
        {
            var client = Start();
            string device = client.Session.DeviceId;
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Pump(client);

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            Assert.IsTrue(ApmClient.GetStats().Collecting);
            Emit(client, 2);
            Pump(client);
            StringAssert.Contains(device, StoredText());
            Assert.AreEqual(device, PlayerPrefs.GetString(DeviceKey), "stored again with consent");
            Assert.IsTrue(PlayerPrefs.HasKey(SessionKey));
        }

        [Test]
        public void GrantedThenUndecided_KeepsStoredData_CollectsAndUploadsNothing()
        {
            var client = Start();
            Emit(client, 2);
            ApmClient.Flush();
            Pump(client);
            string stored = StoredText();
            Assert.IsNotEmpty(stored);

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Undecided);
            Emit(client, 2);
            Pump(client);
            Assert.AreEqual(stored, StoredText());
            Assert.IsFalse(ApmClient.GetStats().Collecting);
            client.StorageForTests.RequestUpload();
            Pump(client);
            Assert.AreEqual(UploadPoll.Empty, client.StorageForTests.PollUpload(out _));
            Assert.IsTrue(PlayerPrefs.HasKey(DeviceKey), "Undecided does not delete");
        }

        [Test]
        public void Choice_PersistsAcrossRelaunch_AndWinsOverTheDefault()
        {
            Start();
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            ApmClient.SetConsent(ConsentCategory.Analytics, ConsentState.Granted);

            var client = Relaunch(); // default config: technical Granted
            Assert.AreEqual(ConsentState.Denied, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Analytics));
            Assert.IsFalse(ApmClient.GetStats().Collecting);
            Emit(client, 2);
            Pump(client);
            Assert.IsFalse(Directory.Exists(_dir));

            // Before Initialize the stored choice is reported too.
            ApmClient.ResetForTests();
            Assert.AreEqual(ConsentState.Denied, ApmClient.GetConsent(ConsentCategory.Technical));

            // An explicit Undecided is a choice as well.
            Start();
            ApmClient.SetConsent(ConsentState.Undecided);
            Relaunch();
            Assert.AreEqual(ConsentState.Undecided, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual(ConsentState.Undecided, ApmClient.GetConsent(ConsentCategory.Analytics));
        }

        [Test]
        public void RememberConsentOff_StoresNothing_DefaultsApplyOnRelaunch()
        {
            var config = NewConfig();
            config.RememberConsent = false;
            Start(config);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Assert.IsFalse(PlayerPrefs.HasKey(ConsentStore.TechnicalKey));

            config = NewConfig();
            config.RememberConsent = false;
            Relaunch(config);
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Technical));
        }

        [Test]
        public void StoredDenied_AtLaunch_DeletesFilesLeftBehind()
        {
            var client = Start();
            Emit(client, 4);
            Pump(client);
            Assert.IsTrue(AnyFiles);
            // Killed before the worker deleted anything: choice stored, files still there.
            PlayerPrefs.SetInt(ConsentStore.TechnicalKey, (int)ConsentState.Denied);
            PlayerPrefs.Save();
            client.StorageForTests.SimulateCrashForTests();

            client = Relaunch();
            Pump(client);
            Assert.IsFalse(Directory.Exists(_dir));
            Assert.IsFalse(PlayerPrefs.HasKey(DeviceKey));
        }

        [Test]
        public void SetConsentBeforeInitialize_WinsOverDefaults_AndIsStored()
        {
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Assert.AreEqual(ConsentState.Denied, ApmClient.GetConsent(ConsentCategory.Technical));
            Start();
            Assert.AreEqual(ConsentState.Denied, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual((int)ConsentState.Denied, PlayerPrefs.GetInt(ConsentStore.TechnicalKey));
            Assert.IsFalse(PlayerPrefs.HasKey(ConsentStore.AnalyticsKey), "only the category that was set");
        }

        [Test]
        public void LegacySingleConsent_CoversBothCategories()
        {
            var config = NewConfig();
            config.Consent = ConsentState.Undecided; // pre-APM-14 way to start without consent
            Assert.AreEqual(ConsentState.Undecided, config.DefaultTechnicalConsent);
            Assert.AreEqual(ConsentState.Undecided, config.DefaultAnalyticsConsent);
            Start(config);
            Assert.AreEqual(ConsentState.Undecided, ApmClient.Consent);

            ApmClient.SetConsent(ConsentState.Granted);
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Analytics));
            Assert.AreEqual(ConsentState.Undecided, ConsentState.Unknown, "old name kept");
        }

        [Test]
        public void InvalidState_IsIgnored()
        {
            Start();
            ApmClient.SetConsent(ConsentCategory.Technical, (ConsentState)7);
            Assert.AreEqual(ConsentState.Granted, ApmClient.GetConsent(ConsentCategory.Technical));
            Assert.IsTrue(ApmClient.GetStats().Collecting);
        }

        [Test]
        public void AnalyticsDenied_DoesNotTouchTechnicalData()
        {
            var client = Start();
            Emit(client, 2);
            Pump(client);
            ApmClient.SetConsent(ConsentCategory.Analytics, ConsentState.Denied);
            Pump(client);
            Assert.IsTrue(AnyFiles);
            Assert.IsTrue(ApmClient.GetStats().Collecting);
            Assert.AreEqual(ConsentState.Denied, ApmClient.GetStats().AnalyticsConsent);
        }

        [Test]
        public void ResetDeviceId_NewIdNewSession_LocalEventsDeleted_SeqRestarts()
        {
            var client = Start();
            string oldDevice = client.Session.DeviceId;
            string oldSession = client.Session.SessionId;
            Emit(client, 3);
            Pump(client);
            StringAssert.Contains(oldDevice, StoredText());

            string newDevice = ApmClient.ResetDeviceId();
            Assert.IsFalse(string.IsNullOrEmpty(newDevice));
            Assert.AreNotEqual(oldDevice, newDevice);
            Assert.AreEqual(newDevice, ApmClient.GetDeviceId());
            Assert.AreNotEqual(oldSession, client.Session.SessionId, "a new session: nothing links old and new id");
            Assert.AreEqual(newDevice, PlayerPrefs.GetString(DeviceKey));

            Emit(client, 1);
            Pump(client);
            string text = StoredText();
            StringAssert.DoesNotContain(oldDevice, text);
            StringAssert.Contains(newDevice, text);
            StringAssert.Contains("\"seq\":1,", text);
            Assert.AreEqual(1, Regex.Matches(text, "\"seq\":").Count);
        }

        [Test]
        public void ResetDeviceId_BeforeInitialize_ClearsStoredEventsAtStart()
        {
            var client = Start();
            string oldDevice = client.Session.DeviceId;
            Emit(client, 3);
            Pump(client);

            ApmClient.ResetForTests();
            DeviceInfo.ClearCacheForTests();
            string newDevice = ApmClient.ResetDeviceId();
            Assert.AreNotEqual(oldDevice, newDevice);
            Assert.IsFalse(PlayerPrefs.HasKey(DeviceKey), "old id removed; the new one is stored by Initialize");

            client = Start();
            Pump(client);
            Assert.AreEqual(newDevice, client.Session.DeviceId);
            Assert.AreEqual(newDevice, PlayerPrefs.GetString(DeviceKey));
            StringAssert.DoesNotContain(oldDevice, StoredText());
        }

        [Test]
        public void ResetDeviceId_WithoutConsent_StoresNoId()
        {
            Start(NewConfig().UseOptInConsent());
            string id = ApmClient.ResetDeviceId();
            Assert.IsFalse(string.IsNullOrEmpty(id));
            Assert.IsFalse(PlayerPrefs.HasKey(DeviceKey));
        }

        [Test]
        public void ExceptionsLoggedWhileDenied_AreNotCollected()
        {
            var client = Start();
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            LogAssert.Expect(LogType.Error, "apm14 test error while denied");
            Debug.LogError("apm14 test error while denied");
            Assert.AreEqual(0, ApmClient.GetStats().PendingEvents);

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            LogAssert.Expect(LogType.Error, "apm14 test error while granted");
            Debug.LogError("apm14 test error while granted");
            Assert.AreEqual(1, ApmClient.GetStats().PendingEvents, "the exception collector runs again");
        }

        [Test]
        public void WhileDenied_UpdateDoesNotAllocate()
        {
            var client = Start(NewConfig().UseOptInConsent());
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            for (int i = 0; i < 10; i++)
                client.UpdateForTests(); // warm up

            Assert.That(() =>
            {
                for (int i = 0; i < 100; i++)
                    client.UpdateForTests();
            }, Is.Not.AllocatingGCMemory());
            Assert.Less(ApmClient.GetStats().LastUpdateMs, 1.0);
        }

        [Test]
        public void WhileDenied_EnqueueDoesNotAllocate()
        {
            var client = Start();
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            var evt = new ExceptionEvent();
            client.EnqueueEvent(evt);
            Assert.That(() => client.EnqueueEvent(evt), Is.Not.AllocatingGCMemory());
        }
    }
}
