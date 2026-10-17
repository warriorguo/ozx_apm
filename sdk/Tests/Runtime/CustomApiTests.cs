using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-6 through the public API (EditMode: needs the Unity runtime):
    /// TrackEvent, RecordMetric, timers, consent, and the time line wired to
    /// the log callback.
    /// </summary>
    [TestFixture]
    public class CustomApiTests
    {
        private string _dir;

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
            TimerRegistry.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_custom_" + Guid.NewGuid().ToString("N"));
        }

        [TearDown]
        public void TearDown()
        {
            ApmClient.ResetForTests();
            ResetPrefs();
            GameContext.ResetForTests();
            TimerRegistry.ResetForTests();
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
                InitialUploadDelaySeconds = 3600f
            };
            configure?.Invoke(config);
            ApmClient.StoragePathForTests = _dir;
            ApmClient.Initialize(config);
            Assert.IsTrue(ApmClient.IsInitialized);
            return ApmClient.Instance;
        }

        private string Stored(ApmClient client)
        {
            ApmClient.Flush();
            client.StorageForTests.Pump();
            return Directory.Exists(_dir)
                ? string.Concat(Directory.GetFiles(_dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText))
                : "";
        }

        [Test]
        public void BeforeInitialize_NothingIsRecorded_NothingThrows()
        {
            Assert.IsFalse(ApmClient.TrackEvent("room_enter"));
            Assert.IsFalse(ApmClient.TrackEvent("room_enter", "roomId", "1"));
            Assert.IsFalse(ApmClient.TrackEvent("room_enter", new Dictionary<string, string> { { "a", "b" } }));
            Assert.IsFalse(ApmClient.RecordMetric("load_ms", 5));
            var t = ApmClient.StartTimer("room_switch_ms");
            Assert.IsTrue(t.IsValid);
            Assert.GreaterOrEqual(ApmClient.StopTimer(t), 0);
            Assert.AreEqual(-1, ApmClient.StopTimer("never_started"));
            Assert.AreEqual(-1, ApmClient.StopTimer(default(ApmTimer)));
            ApmClient.RecordBreadcrumb("x");
        }

        [Test]
        public void EventsMetricsAndTimers_AreStored()
        {
            var client = Start();
            ApmClient.SetContext("roomId", "12");
            Assert.IsTrue(ApmClient.TrackEvent("room_enter", "seed", "99"));
            Assert.IsTrue(ApmClient.TrackEvent("wave_start", new Dictionary<string, string> { { "wave", "3" }, { "bad key", "x" } }));
            Assert.IsTrue(ApmClient.RecordMetric("gold", 15, "count"));
            var t = ApmClient.StartTimer("room_switch_ms");
            ApmClient.StartTimer("load_ms");
            Assert.GreaterOrEqual(ApmClient.StopTimer(t), 0);
            Assert.GreaterOrEqual(ApmClient.StopTimer("load_ms"), 0);

            string stored = Stored(client);
            StringAssert.Contains("\"type\":\"custom_event\"", stored);
            StringAssert.Contains("\"name\":\"room_enter\",\"count\":1,\"props\":{\"seed\":\"99\"},\"ctx\":{\"roomId\":\"12\"}", stored);
            StringAssert.Contains("\"name\":\"wave_start\",\"count\":1,\"props\":{\"wave\":\"3\"}", stored);
            StringAssert.Contains("\"type\":\"custom_metric\"", stored);
            StringAssert.Contains("\"name\":\"room_switch_ms\",\"unit\":\"ms\",\"count\":1", stored);
            StringAssert.Contains("\"name\":\"load_ms\",\"unit\":\"ms\",\"count\":1", stored);
            StringAssert.Contains("\"name\":\"gold\",\"unit\":\"count\",\"count\":1,\"sum\":15,\"min\":15,\"max\":15", stored);

            var stats = ApmClient.GetStats();
            Assert.AreEqual(2, stats.CustomEvents);
            Assert.AreEqual(3, stats.MetricSamples);
            Assert.AreEqual(1, stats.CustomRejected, "the invalid property key");
        }

        [Test]
        public void TimerAcrossAPause_IsNotRecorded()
        {
            var client = Start();
            var t = ApmClient.StartTimer("room_switch_ms");
            client.PauseForTests(true);
            client.PauseForTests(false);
            Assert.GreaterOrEqual(ApmClient.StopTimer(t), 0);
            Assert.AreEqual(1, ApmClient.GetStats().TimersSpanningPause);
            StringAssert.DoesNotContain("room_switch_ms", Stored(client));
        }

        [Test]
        public void Disabled_OrWithoutConsent_NothingIsRecorded()
        {
            var client = Start(c => c.EnableCustomEvents = false);
            Assert.IsFalse(ApmClient.TrackEvent("room_enter"));
            Assert.IsFalse(ApmClient.RecordMetric("load_ms", 5));
            ApmClient.ResetForTests();

            client = Start();
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Undecided);
            Assert.IsFalse(ApmClient.TrackEvent("room_enter"));
            Assert.IsFalse(ApmClient.RecordMetric("load_ms", 5));
            ApmClient.StopTimer(ApmClient.StartTimer("t_ms"));
            Assert.AreEqual(0, ApmClient.GetStats().MetricSamples);
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Granted);
            Assert.IsTrue(ApmClient.TrackEvent("room_enter"));
        }

        // The SDK's log callback feeds the time line; exceptions carry it.
        [Test]
        public void LoggedWarning_IsOnTheTimelineOfTheNextException()
        {
            var client = Start();
            var ec = client.ExceptionCollectorForTests;
            var captured = new List<ExceptionEvent>();
            ec.EnqueueForTests = captured.Add;
            ApmClient.TrackEvent("room_enter", "roomId", "12");
            ec.HandleLogMessage("Asset not found: anim/arack_red", "", LogType.Warning);
            ec.HandleLogMessage("Plain log", "", LogType.Log);
            ec.HandleLogMessage("NullReferenceException: Object reference not set to an instance of an object",
                "Ozx.Pool.PoolFactory.Instantiate ()\n", LogType.Exception);
            var crumbs = captured.Single().breadcrumbs;
            Assert.AreEqual(2, crumbs.Count);
            StringAssert.Contains("] Event: room_enter roomId=12", crumbs[0]);
            StringAssert.Contains("] Warning: Asset not found: anim/arack_red", crumbs[1]);
            Assert.AreEqual(1, ApmClient.GetStats().LogLinesSeen);

            // Denied: the time line is forgotten with everything else.
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            Assert.AreEqual(0, client.Timeline.Snapshot(50, 32, 200, 300).Count);
        }
    }
}
