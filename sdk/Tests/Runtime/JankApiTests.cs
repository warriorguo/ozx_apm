using System;
using System.IO;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Collectors;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-21 through ApmClient (EditMode: needs the Unity runtime): a jank
    /// in progress is stored when the app pauses (before the pause flush),
    /// with its own frame as duration, the context of its start and the
    /// recent breadcrumbs; the frame after the resume is not a jank; the
    /// stats show the counters.
    /// </summary>
    [TestFixture]
    public class JankApiTests
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
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_jank_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_dir);
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
                LogLevel = LogLevel.None,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                InitialUploadDelaySeconds = 3600f,
                EnableMemoryBreakdown = false,
            };
            ApmClient.StoragePathForTests = Path.Combine(_dir, "events");
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

        private static string[] Janks(string stored) =>
            stored.Split('\n').Where(l => l.StartsWith("{\"type\":\"jank\"")).ToArray();

        [Test]
        public void JankInProgress_IsStoredAtPause_AndTheResumeFrameIsNotAJank()
        {
            var client = Start();
            var detector = client.JankDetectorForTests;
            Assert.IsNotNull(detector);
            int target = Application.targetFrameRate, vSync = QualitySettings.vSyncCount;
            for (int i = 0; i < 60; i++)
                detector.UpdateWith(33.4f, target, vSync);
            Assert.AreEqual(33.4f, ApmClient.GetStats().JankExpectedFrameMs, 0.01f);

            ApmClient.SetContext("roomId", "r-21");
            ApmClient.RecordBreadcrumb("before-jank");
            detector.UpdateWith(150f, target, vSync);
            ApmClient.SetContext("roomId", "r-22");
            client.PauseForTests(true);

            var janks = Janks(Stored(client));
            Assert.AreEqual(1, janks.Length);
            StringAssert.Contains("\"duration_ms\":150.00", janks[0]);
            StringAssert.Contains("\"max_frame_ms\":150.00", janks[0]);
            StringAssert.Contains("before-jank", janks[0]);
            StringAssert.Contains("\"roomId\":\"r-21\"", janks[0], "the context when it began");

            client.PauseForTests(false);
            detector.UpdateWith(6210f, target, vSync);
            for (int i = 0; i < 300; i++)
                detector.UpdateWith(33.4f, target, vSync);
            client.PauseForTests(true);
            Assert.AreEqual(1, Janks(Stored(client)).Length);

            var stats = ApmClient.GetStats();
            Assert.AreEqual(1, stats.JanksDetected);
            Assert.AreEqual(1, stats.JanksSent);
            Assert.AreEqual(0, stats.JanksDropped);
            Assert.AreEqual(150f, stats.LastJankDurationMs);
            Assert.AreEqual(1, stats.LastJankFrames);
            Assert.AreEqual(1, stats.LastJankSlowFrames);
        }
    }
}
