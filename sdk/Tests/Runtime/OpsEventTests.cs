using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using OzxApm.Utils;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-12: the ops queue is its own OfflineStorage (directory, sequence,
    /// cap) gated by analytics consent, with its own uploader; each consent
    /// category's data is written, uploaded and deleted by its own consent.
    /// Pumped synchronously (no Unity runtime needed).
    /// </summary>
    [TestFixture]
    public class OpsQueueTests
    {
        private string _root;
        private string TechDir => Path.Combine(_root, "ozx_apm_offline");
        private string OpsDir => Path.Combine(_root, OfflineStorage.OpsDirectoryName);
        private readonly List<OfflineStorage> _stores = new List<OfflineStorage>();

        [SetUp]
        public void SetUp()
        {
            _root = Path.Combine(Path.GetTempPath(), "ozx_apm_ops_" + Guid.NewGuid().ToString("N"));
            GameContext.ResetForTests();
        }

        [TearDown]
        public void TearDown()
        {
            foreach (var s in _stores)
                s.Shutdown(0);
            _stores.Clear();
            if (Directory.Exists(_root))
                Directory.Delete(_root, true);
            GameContext.ResetForTests();
        }

        private static ApmConfig Config(ConsentState technical, ConsentState analytics)
        {
            return new ApmConfig
            {
                ServerUrl = "http://test",
                BatchSize = 5,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                EnableCompression = false,
                InitialUploadDelaySeconds = 0f,
                LogLevel = LogLevel.None,
                DefaultTechnicalConsent = technical,
                DefaultAnalyticsConsent = analytics
            };
        }

        private OfflineStorage Tech(ApmConfig c)
        {
            var s = new OfflineStorage(c, TechDir, false);
            _stores.Add(s);
            return s;
        }

        private OfflineStorage Ops(ApmConfig c)
        {
            var s = OfflineStorage.ForOps(c, OpsDir, false);
            _stores.Add(s);
            return s;
        }

        internal static OpsEvent Op(string name, int i = 0)
        {
            return new OpsEvent
            {
                name = name, timestamp = 1792497600000 + i, app_version = "1.0", platform = "Android",
                session_id = "s1", build_type = "release", player_id = "p1", channel = "googleplay", country = "DE"
            };
        }

        private static PerfSampleEvent Perf()
        {
            return new PerfSampleEvent { fps = 60, timestamp = 1792497600000, app_version = "1.0", platform = "Android", session_id = "s1", device_id = "d1" };
        }

        private static string Text(string dir)
        {
            return Directory.Exists(dir)
                ? string.Concat(Directory.GetFiles(dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        [Test]
        public void OpsQueue_NeedsAnalyticsConsent_NotTechnical()
        {
            var undecided = Config(ConsentState.Granted, ConsentState.Undecided);
            var ops = Ops(undecided);
            Assert.AreEqual(ConsentCategory.Analytics, ops.Category);
            ops.Enqueue(Op("run_start"));
            ops.Flush();
            ops.Pump();
            Assert.IsFalse(Directory.Exists(OpsDir), "nothing written without analytics consent, whatever the technical consent");

            var analyticsOnly = Config(ConsentState.Denied, ConsentState.Granted);
            var ops2 = OfflineStorage.ForOps(analyticsOnly, OpsDir + "2", false);
            _stores.Add(ops2);
            var tech = Tech(analyticsOnly);
            ops2.Enqueue(Op("run_start"));
            tech.Enqueue(Perf());
            ops2.Pump();
            tech.Pump();
            StringAssert.Contains("\"name\":\"run_start\"", Text(OpsDir + "2"), "analytics granted, technical denied: ops events are written");
            Assert.IsFalse(Directory.Exists(TechDir), "and technical data is not");
        }

        [Test]
        public void ClearOfOneQueue_LeavesTheOther()
        {
            var c = Config(ConsentState.Granted, ConsentState.Granted);
            var tech = Tech(c);
            var ops = Ops(c);
            for (int i = 0; i < 7; i++)
            {
                tech.Enqueue(Perf());
                ops.Enqueue(Op("e", i));
            }
            tech.Pump();
            ops.Pump();
            Assert.IsTrue(Directory.Exists(TechDir) && Directory.Exists(OpsDir));

            ops.Clear(true); // analytics denied
            ops.Pump();
            tech.Pump();
            Assert.IsFalse(Directory.Exists(OpsDir), "the ops directory is gone");
            Assert.That(Text(TechDir), Does.Contain("\"fps\""), "technical data untouched");

            ops.Enqueue(Op("again"));
            ops.Pump();
            tech.Clear(true); // technical denied
            tech.Pump();
            ops.Pump();
            Assert.IsFalse(Directory.Exists(TechDir));
            StringAssert.Contains("\"name\":\"again\"", Text(OpsDir), "ops data untouched");
        }

        [Test]
        public void EachQueueHasItsOwnSequence()
        {
            var c = Config(ConsentState.Granted, ConsentState.Granted);
            var tech = Tech(c);
            var ops = Ops(c);
            for (int i = 0; i < 3; i++)
                tech.Enqueue(Perf());
            ops.Enqueue(Op("a"));
            ops.Enqueue(Op("b"));
            tech.Pump();
            ops.Pump();
            Assert.AreEqual(3, tech.LastSeq);
            Assert.AreEqual(2, ops.LastSeq);
            StringAssert.Contains("\"seq\":1,", Text(OpsDir));
            StringAssert.Contains("\"seq\":2,", Text(OpsDir));
            Assert.IsTrue(File.Exists(Path.Combine(OpsDir, "seq")));
        }

        [Test]
        public void EveryOpsEventGetsItsOwnId()
        {
            var ops = Ops(Config(ConsentState.Granted, ConsentState.Granted));
            for (int i = 0; i < 50; i++)
                ops.Enqueue(Op("x", i));
            ops.Pump();
            var ids = Text(OpsDir).Split('\n').Where(l => l.Length > 0)
                .Select(l => l.Substring(l.IndexOf("\"event_id\":\"", StringComparison.Ordinal) + 12, 32)).ToList();
            Assert.AreEqual(50, ids.Count);
            Assert.AreEqual(50, ids.Distinct().Count());
        }

        [Test]
        public void Cap_IsMaxOpsStorageBytes()
        {
            var c = Config(ConsentState.Granted, ConsentState.Granted);
            c.MaxOpsStorageBytes = 16 * 1024;
            c.MaxOfflineStorageBytes = 5 * 1024 * 1024;
            var ops = Ops(c);
            for (int round = 0; round < 20; round++)
            {
                for (int i = 0; i < 20; i++)
                {
                    var e = Op("big", i);
                    e.props = new[] { "pad", new string('x', 200) };
                    ops.Enqueue(e);
                }
                ops.Pump();
            }
            Assert.LessOrEqual(ops.GetStorageSize(), 16 * 1024 + 4096);
            Assert.Greater(ops.DroppedEventCount, 0);
        }

        [Test]
        public void HasData_FollowsTheQueue()
        {
            var ops = Ops(Config(ConsentState.Granted, ConsentState.Granted));
            Assert.IsTrue(ops.HasData, "unknown until the first scan");
            ops.Pump();
            Assert.IsFalse(ops.HasData, "empty directory");
            ops.Enqueue(Op("a"));
            Assert.IsTrue(ops.HasData, "set by Enqueue at once");
            ops.Pump();
            Assert.IsTrue(ops.HasData, "written, not uploaded");
            ops.Clear();
            ops.Pump();
            Assert.IsFalse(ops.HasData);
        }

        private sealed class Request : IUploadRequest
        {
            public bool IsDone { get; set; }
            public long ResponseCode { get; set; }
            public bool TransportError => false;
            public string Error => null;
            public string ResponseBody => null;
            public string RetryAfter => null;
            public byte[] Payload;
            public int CopyResponse(byte[] dst) => 0;
            public void Start() { }
            public void Abort() { }
            public void Dispose() { }
        }

        private sealed class Transport : IUploadTransport
        {
            public readonly List<Request> Requests = new List<Request>();
            public IUploadRequest Create(string url, byte[] payload, int length, bool compressed, string appKey, int timeoutSeconds)
            {
                var r = new Request { Payload = payload.Take(length).ToArray() };
                Requests.Add(r);
                return r;
            }
        }

        // The upload gate is per category: with analytics granted and technical
        // denied only the ops queue uploads, and the other way round.
        [Test]
        public void UploadGate_AnalyticsOnly_OnlyOpsUploads() => UploadGate(ConsentState.Denied, ConsentState.Granted, false, true);

        [Test]
        public void UploadGate_TechnicalOnly_OnlyTechnicalUploads() => UploadGate(ConsentState.Granted, ConsentState.Undecided, true, false);

        [Test]
        public void UploadGate_Both_BothUpload() => UploadGate(ConsentState.Granted, ConsentState.Granted, true, true);

        private void UploadGate(ConsentState technical, ConsentState analytics, bool techUploads, bool opsUploads)
        {
            var c = Config(ConsentState.Granted, ConsentState.Granted);
            var tech = Tech(c);
            var ops = Ops(c);
            tech.Enqueue(Perf());
            ops.Enqueue(Op("a"));
            tech.Flush();
            ops.Flush();
            tech.Pump();
            ops.Pump();
            c.SetConsentState(ConsentCategory.Technical, technical);
            c.SetConsentState(ConsentCategory.Analytics, analytics);

            float now = 100f;
            var techT = new Transport();
            var opsT = new Transport();
            var techR = new EventReporter(c, tech, null, techT, () => now, () => NetworkReachability.ReachableViaLocalAreaNetwork, new System.Random(1));
            var opsR = new EventReporter(c, ops, null, opsT, () => now, () => NetworkReachability.ReachableViaLocalAreaNetwork, new System.Random(1));
            for (int i = 0; i < 6; i++)
            {
                techR.Update();
                opsR.Update();
                tech.Pump();
                ops.Pump();
            }
            Assert.AreEqual(techUploads ? 1 : 0, techT.Requests.Count, "technical uploads");
            Assert.AreEqual(opsUploads ? 1 : 0, opsT.Requests.Count, "ops uploads");
            if (opsUploads)
                StringAssert.Contains("\"ops\":true", System.Text.Encoding.UTF8.GetString(opsT.Requests[0].Payload));
        }
    }

    /// <summary>APM-12: what an ops event carries on the wire, and what it never does.</summary>
    [TestFixture]
    public class OpsSerializationTests
    {
        [SetUp]
        public void SetUp() => GameContext.ResetForTests();

        [TearDown]
        public void TearDown() => GameContext.ResetForTests();

        [Test]
        public void OpsEvent_CarriesOnlyWhatTheOpsTablesKeep()
        {
            GameContext.Set("runId", "r-7");
            var e = OpsQueueTests.Op("ad_show");
            e.event_id = "0123456789abcdef0123456789abcdef";
            e.seq = 42;
            e.props = new[] { "placement", "revive", "revenue", "0.012345", "rewarded", "true" };
            e.prop_kinds = new[] { PropKinds.String, PropKinds.Number, PropKinds.Bool };
            e.ctx = GameContext.Current;
            // Technical fields a careless stamp might set: never on the wire.
            e.device_id = "dev-1";
            e.device_model = "SM-A336B";
            e.os_version = "16";
            e.gpu = "Mali";
            e.ram_class = "6GB";
            e.net_type = "WiFi";
            e.scene = "Main";
            e.game_t = 1.5;
            e.frame = 90;
            string json = JsonSerializer.SerializeEvent(e);
            Assert.AreEqual("{\"type\":\"custom_event\",\"event_id\":\"0123456789abcdef0123456789abcdef\",\"seq\":42,\"timestamp\":1792497600000," +
                "\"app_version\":\"1.0\",\"platform\":\"Android\",\"session_id\":\"s1\",\"build_type\":\"release\",\"name\":\"ad_show\"," +
                "\"count\":1,\"ops\":true,\"player_id\":\"p1\",\"channel\":\"googleplay\",\"country\":\"DE\"," +
                "\"props\":{\"placement\":\"revive\",\"revenue\":0.012345,\"rewarded\":true},\"ctx\":{\"runId\":\"r-7\"}}", json);
        }

        // The ops events' context whitelist (ApmConfig.OpsContextKeys): the rest
        // of the context never leaves the device with an ops event.
        [Test]
        public void ContextSelect_KeepsOnlyTheWhitelistedKeys()
        {
            GameContext.Set("runId", "r-1");
            GameContext.Set("masterSeed", "18446744");
            GameContext.Set("chapter", "2");
            var all = GameContext.Current;
            var keys = new HashSet<string>(new[] { "runId", "chapter", "room" }, StringComparer.Ordinal);
            var ops = all.Select(keys);
            Assert.AreEqual("{\"chapter\":\"2\",\"runId\":\"r-1\"}", ops.Json);
            Assert.AreSame(all, all.Select(null), "null: the whole context");
            Assert.AreSame(ContextSnapshot.Empty, all.Select(new HashSet<string> { "other" }));
            var onlyKept = ops.Select(keys);
            Assert.AreSame(ops, onlyKept, "nothing to drop: no copy");
        }

        [Test]
        public void OpsEvent_IdFromTheWorker_AndOptionalFieldsLeftOut()
        {
            var e = new OpsEvent { name = "x", timestamp = 1, app_version = "1", platform = "iOS", session_id = "s", player_id = "p", id_hi = 1, id_lo = 2 };
            string json = JsonSerializer.SerializeEvent(e);
            StringAssert.Contains("\"event_id\":\"00000000000000010000000000000002\"", json);
            StringAssert.DoesNotContain("channel", json);
            StringAssert.DoesNotContain("country", json);
            StringAssert.DoesNotContain("props", json);
            StringAssert.DoesNotContain("device", json);
        }
    }

    /// <summary>APM-12: the technical copy of an ops event (AlsoTechnical) is not held back by the custom event budget.</summary>
    [TestFixture]
    public class OpsTechnicalCopyTests
    {
        [Test]
        public void BypassBudget_SendsEveryCallOnItsOwn_AndLeavesTheBudget()
        {
            GameContext.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            var events = new List<BaseEvent>();
            var c = new CustomCollector(new ApmConfig { MaxCustomEventsPerMinute = 3, MaxCustomEventsPerNamePerMinute = 2 }, null, new Timeline());
            c.EnqueueForTests = e => { lock (events) events.Add(e); };
            var clock = new SessionClock();
            clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), "s");
            c.ClockForTests = clock;
            c.ActivateForTests();
            for (int i = 0; i < 25; i++)
                Assert.IsTrue(c.TrackEvent("run_end", new[] { "i", i.ToString() }, null, true));
            var sent = events.OfType<CustomEvent>().ToList();
            Assert.AreEqual(25, sent.Count);
            Assert.IsTrue(sent.All(e => e.count == 1 && !e.aggregated && e.props != null));
            // The budget is intact for ordinary events.
            Assert.IsTrue(c.TrackEvent("room_enter", null));
            Assert.IsTrue(c.TrackEvent("room_enter", null));
            Assert.AreEqual(27, events.OfType<CustomEvent>().Count());
            GameContext.ResetForTests();
        }
    }

    /// <summary>
    /// APM-12 through ApmClient (needs the Unity runtime: EditMode): consent
    /// per category, the player id, the session events, ApmAds, the technical
    /// copy and breadcrumb, cost. The storage workers are pumped by the test;
    /// uploads are off (Editor).
    /// </summary>
    [TestFixture]
    public class ApmClientOpsTests
    {
        private const string DeviceKey = "ozx_apm_device_id";
        private static readonly string[] Keys = { DeviceKey, "ozx_apm_session_id", "ozx_apm_session_start", PlayerIdentity.Key,
            ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey };
        private static readonly string RunStart = "run_start";
        private static readonly string Mode = "mode";
        private static readonly string Hard = "hard";

        private string _dir;
        private string OpsDir => _dir + "_ops";

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
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_ops_client_" + Guid.NewGuid().ToString("N"));
        }

        [TearDown]
        public void TearDown()
        {
            ResetAll();
            foreach (var d in new[] { _dir, OpsDir })
                if (Directory.Exists(d))
                    Directory.Delete(d, true);
        }

        private static ApmConfig NewConfig(ConsentState analytics = ConsentState.Granted, ConsentState technical = ConsentState.Granted)
        {
            return new ApmConfig
            {
                ServerUrl = "http://127.0.0.1:9",
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f,
                DefaultAnalyticsConsent = analytics,
                DefaultTechnicalConsent = technical,
                Channel = "googleplay",
                Country = "de",
                MaxCustomEventsPerMinute = 5
            };
        }

        private ApmClient Start(ApmConfig config = null)
        {
            ApmClient.StoragePathForTests = _dir;
            ApmClient.Initialize(config ?? NewConfig());
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        private ApmClient Relaunch(ApmConfig config = null)
        {
            ApmClient.ResetForTests();
            DeviceInfo.ClearCacheForTests();
            return Start(config);
        }

        private static void Pump(ApmClient client)
        {
            client.StorageForTests.Pump();
            client.OpsStorageForTests?.Pump();
        }

        private static string Text(string dir)
        {
            return Directory.Exists(dir)
                ? string.Concat(Directory.GetFiles(dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        private static string[] Lines(string dir, string name)
        {
            return Text(dir).Split('\n').Where(l => l.Contains("\"name\":\"" + name + "\"")).ToArray();
        }

        [Test]
        public void Defaults_AnalyticsUndecided_NoOpsEventNoPlayerIdStored()
        {
            var client = Start(NewConfig(ConsentState.Undecided));
            Assert.IsFalse(ApmClient.OpsEvent(RunStart).Send(), "not recorded (not even as a breadcrumb)");
            Assert.IsFalse(ApmAds.Show("revive", "admob", 0.01, "USD"));
            Pump(client);
            Assert.IsFalse(Directory.Exists(OpsDir), "nothing of the ops queue without analytics consent");
            Assert.IsFalse(PlayerPrefs.HasKey(PlayerIdentity.Key));
            Assert.AreEqual(0, ApmClient.GetStats().OpsEvents);
            Assert.IsTrue(ApmClient.GetStats().OpsEnabled);
        }

        [Test]
        public void Granted_SessionStartAtLaunch_AndEventsWithThePlayerId()
        {
            var client = Start();
            GameContext.Set("chapter", "3");
            GameContext.Set("masterSeed", "18446744");
            Assert.IsTrue(ApmClient.OpsEvent(RunStart).Str(Mode, Hard).Int("chapter", 3).Send());
            Pump(client);
            string player = ApmClient.GetPlayerId();
            Assert.AreEqual(32, player.Length);
            Assert.AreEqual(player, PlayerPrefs.GetString(PlayerIdentity.Key), "stored with analytics consent");
            Assert.AreNotEqual(client.Session.DeviceId, player, "not the device id");
            var start = Lines(OpsDir, ApmClient.OpsSessionStartEvent).Single();
            StringAssert.Contains("\"session_id\":\"" + client.Session.SessionId + "\"", start);
            var run = Lines(OpsDir, RunStart).Single();
            StringAssert.Contains("\"ops\":true", run);
            StringAssert.Contains("\"player_id\":\"" + player + "\"", run);
            StringAssert.Contains("\"channel\":\"googleplay\"", run);
            StringAssert.Contains("\"country\":\"DE\"", run);
            StringAssert.Contains("\"props\":{\"mode\":\"hard\",\"chapter\":3}", run);
            StringAssert.Contains("\"ctx\":{\"chapter\":\"3\"}", run, "only ApmConfig.OpsContextKeys");
            StringAssert.DoesNotContain("masterSeed", Text(OpsDir));
            StringAssert.DoesNotContain("device_id", run);
            StringAssert.DoesNotContain(client.Session.DeviceId, Text(OpsDir));
            Assert.AreEqual(0, Lines(_dir, RunStart).Length, "an ops event is not a technical custom event");
        }

        [Test]
        public void OpsEvents_AreNeverHeldBackByTheBudget()
        {
            var client = Start();
            for (int i = 0; i < 200; i++)
                Assert.IsTrue(ApmClient.OpsEvent(RunStart).Int("i", i).Send());
            Pump(client);
            Assert.AreEqual(200, Lines(OpsDir, RunStart).Length);
            Assert.AreEqual(201, ApmClient.GetStats().OpsEvents, "200 and the session start");
        }

        [Test]
        public void PlayerId_IsStableAcrossLaunches_AndSeparateFromTheDeviceId()
        {
            Start();
            string player = ApmClient.GetPlayerId();
            string device = ApmClient.GetDeviceId();
            var client = Relaunch();
            Assert.AreEqual(player, ApmClient.GetPlayerId());
            ApmClient.ResetDeviceId();
            Assert.AreEqual(player, ApmClient.GetPlayerId(), "ResetDeviceId does not touch the player id");
            Assert.AreNotEqual(device, ApmClient.GetDeviceId());
            Pump(client);
        }

        [Test]
        public void TechnicalDenied_AnalyticsGranted_OpsCollected_NoTechnicalData()
        {
            var client = Start(NewConfig(ConsentState.Granted, ConsentState.Denied));
            Assert.IsTrue(ApmClient.OpsEvent(RunStart).Send());
            Assert.IsTrue(ApmAds.Show("revive", "AdMob", 0.0123456, "usd"));
            Pump(client);
            Assert.AreEqual(1, Lines(OpsDir, RunStart).Length);
            var show = Lines(OpsDir, ApmAds.ShowEvent).Single();
            StringAssert.Contains("\"placement\":\"revive\",\"network\":\"admob\",\"revenue\":0.012346,\"currency\":\"USD\"", show);
            Assert.IsFalse(Directory.Exists(_dir), "no technical file");
            StringAssert.DoesNotContain("device", Text(OpsDir));
        }

        [Test]
        public void AnalyticsDenied_DeletesOnlyTheOpsData()
        {
            var client = Start();
            ApmClient.OpsEvent(RunStart).Send();
            client.EnqueueEvent(new ExceptionEvent { fingerprint = "f", message = "m", stack = "s", count = 1 });
            Pump(client);
            Assert.IsTrue(Directory.Exists(OpsDir) && Directory.Exists(_dir));
            string player = ApmClient.GetPlayerId();

            ApmClient.SetConsent(ConsentCategory.Analytics, ConsentState.Denied);
            Assert.AreNotEqual(player, ApmClient.GetPlayerId(), "granting again later must not bring back a possibly deleted id");
            ApmClient.OpsEvent("ops_only_denied").Send();
            Assert.IsFalse(client.Timeline.Snapshot(10, 10, 200, 300).Any(c => c.Contains("ops_only_denied")),
                "no breadcrumb of an ops event without analytics consent");
            Pump(client);
            Assert.IsFalse(Directory.Exists(OpsDir), "ops directory deleted");
            Assert.IsFalse(PlayerPrefs.HasKey(PlayerIdentity.Key), "stored player id removed");
            Assert.IsTrue(Directory.Exists(_dir), "technical data untouched");
            Assert.IsTrue(PlayerPrefs.HasKey(DeviceKey));
            Assert.IsFalse(ApmClient.OpsEvent(RunStart).Send());
            Pump(client);
            Assert.IsFalse(Directory.Exists(OpsDir), "nothing collected while denied");

            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Pump(client);
            Assert.IsFalse(Directory.Exists(_dir));
        }

        [Test]
        public void TechnicalDenied_LeavesTheOpsData()
        {
            var client = Start();
            ApmClient.OpsEvent(RunStart).Send();
            Pump(client);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Pump(client);
            Assert.AreEqual(1, Lines(OpsDir, RunStart).Length);
            Assert.IsTrue(PlayerPrefs.HasKey(PlayerIdentity.Key));
        }

        [Test]
        public void GrantedLater_StoresThePlayerId_AndStartsTheSession()
        {
            var client = Start(NewConfig(ConsentState.Undecided));
            string player = ApmClient.GetPlayerId();
            Pump(client);
            Assert.IsFalse(Directory.Exists(OpsDir));
            ApmClient.SetConsent(ConsentCategory.Analytics, ConsentState.Granted);
            Pump(client);
            Assert.AreEqual(player, PlayerPrefs.GetString(PlayerIdentity.Key));
            Assert.AreEqual(1, Lines(OpsDir, ApmClient.OpsSessionStartEvent).Length, "the session counts from the moment of consent");
        }

        [Test]
        public void PauseSendsSessionEnd_ALongBackgroundStartsANewSession()
        {
            var client = Start();
            client.Session.BackgroundTimeoutSeconds = 0f;
            string first = client.Session.SessionId;
            client.PauseForTests(true);
            System.Threading.Thread.Sleep(20);
            client.PauseForTests(false);
            Pump(client);
            var end = Lines(OpsDir, ApmClient.OpsSessionEndEvent).Single();
            StringAssert.Contains("\"session_id\":\"" + first + "\"", end);
            StringAssert.Contains("\"foreground_ms\":", end);
            StringAssert.Contains("\"reason\":\"pause\"", end);
            Assert.AreNotEqual(first, client.Session.SessionId, "a new session after the background");
            var starts = Lines(OpsDir, ApmClient.OpsSessionStartEvent);
            Assert.AreEqual(2, starts.Length);
            Assert.AreEqual(1, starts.Count(s => s.Contains("\"session_id\":\"" + client.Session.SessionId + "\"")));
            Assert.AreEqual(1, starts.Count(s => s.Contains("\"session_id\":\"" + first + "\"")));
        }

        // A new session started on focus (focus lost longer than the timeout,
        // no pause): the old one ends with its own id, the new one starts.
        [Test]
        public void FocusTimeout_EndsTheOldSession_StartsTheNew()
        {
            var client = Start();
            client.Session.BackgroundTimeoutSeconds = 0f;
            string first = client.Session.SessionId;
            client.FocusForTests(false);
            System.Threading.Thread.Sleep(20);
            client.FocusForTests(true);
            Pump(client);
            Assert.AreNotEqual(first, client.Session.SessionId);
            var end = Lines(OpsDir, ApmClient.OpsSessionEndEvent).Single();
            StringAssert.Contains("\"session_id\":\"" + first + "\"", end);
            StringAssert.Contains("\"reason\":\"timeout\"", end);
            Assert.AreEqual(1, Lines(OpsDir, ApmClient.OpsSessionStartEvent).Count(s => s.Contains(client.Session.SessionId)));
        }

        [Test]
        public void AlsoTechnical_SendsATechnicalCopy_OtherwiseOnlyABreadcrumb()
        {
            var client = Start();
            Assert.IsTrue(ApmClient.OpsEvent("run_end").Str("result", "win").AlsoTechnical().Send());
            Assert.IsTrue(ApmClient.OpsEvent("shop_open").Send());
            Pump(client);
            Assert.AreEqual(1, Lines(OpsDir, "run_end").Length);
            var tech = Lines(_dir, "run_end").Single();
            StringAssert.DoesNotContain("\"ops\":true", tech);
            StringAssert.Contains("\"device_id\":", tech);
            Assert.AreEqual(0, Lines(_dir, "shop_open").Length);
            var crumbs = client.Timeline.Snapshot(10, 10, 200, 300);
            Assert.IsTrue(crumbs.Any(c => c.Contains("Event: shop_open")), "an ops event is a breadcrumb");
        }

        [Test]
        public void ResetPlayerId_NewIdNewSession_LocalOpsEventsDeleted()
        {
            var client = Start();
            string old = ApmClient.GetPlayerId();
            string session = client.Session.SessionId;
            ApmClient.OpsEvent(RunStart).Send();
            Pump(client);
            string fresh = ApmClient.ResetPlayerId();
            Pump(client);
            Assert.AreNotEqual(old, fresh);
            Assert.AreEqual(fresh, PlayerPrefs.GetString(PlayerIdentity.Key));
            Assert.AreNotEqual(session, client.Session.SessionId);
            StringAssert.DoesNotContain(old, Text(OpsDir));
            Assert.AreEqual(0, Lines(OpsDir, RunStart).Length);
            var start = Lines(OpsDir, ApmClient.OpsSessionStartEvent).Single();
            StringAssert.Contains(fresh, start);
            Assert.AreEqual(0, Lines(OpsDir, ApmClient.OpsSessionEndEvent).Length, "no end pairing the old session with the new id");
        }

        [Test]
        public void OpsDisabled_NothingOfTheQueue()
        {
            var config = NewConfig();
            config.EnableOpsEvents = false;
            var client = Start(config);
            Assert.IsFalse(ApmClient.OpsEvent(RunStart).Send());
            Pump(client);
            Assert.IsFalse(Directory.Exists(OpsDir));
            Assert.IsNull(client.OpsStorageForTests);
            Assert.IsFalse(ApmClient.GetStats().OpsEnabled);
        }

        [Test]
        public void IdleUpdate_WithTheOpsQueue_DoesNotAllocate()
        {
            // Technical denied: the frame is the ops queue's step alone (the
            // collectors' own cost is measured by their tests).
            var client = Start(NewConfig(ConsentState.Granted, ConsentState.Denied));
            Pump(client);
            client.UpdateForTests();
            Assert.That(() =>
            {
                for (int i = 0; i < 100; i++)
                    client.UpdateForTests();
            }, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void OpsEvent_WithoutAnyConsent_DoesNotAllocate()
        {
            Start(NewConfig(ConsentState.Denied, ConsentState.Denied));
            ApmClient.OpsEvent(RunStart).Str(Mode, Hard).Send();
            Assert.That(() =>
            {
                ApmClient.OpsEvent(RunStart).Str(Mode, Hard).Send();
            }, Is.Not.AllocatingGCMemory());
        }
    }
}
