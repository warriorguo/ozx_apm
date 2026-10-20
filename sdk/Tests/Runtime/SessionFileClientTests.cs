using System;
using System.IO;
using System.Linq;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using OzxApm.Utils;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-13 through ApmClient (EditMode: needs the Unity runtime): the
    /// build-type gate at run time (a release player never creates the
    /// uploader, whatever is configured), the upload in the Editor, consent and
    /// device id reset, and no allocation per frame.
    /// </summary>
    [TestFixture]
    public class SessionFileClientTests
    {
        private static readonly string[] Keys = { "ozx_apm_device_id", "ozx_apm_session_id", "ozx_apm_session_start", PlayerIdentity.Key,
            ConsentStore.TechnicalKey, ConsentStore.AnalyticsKey };

        private string _root;
        private string Queue => Path.Combine(_root, "queue");
        private FakeSessionServer _server;

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
            _root = Path.Combine(Path.GetTempPath(), "ozx_apm_sfc_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_root);
            _server = new FakeSessionServer();
            ApmClient.StoragePathForTests = Path.Combine(_root, "events");
            ApmClient.SessionFilesPathForTests = Queue;
            ApmClient.SessionFileTransportForTests = _server;
            ApmClient.SessionFileUploaderSetupForTests = u =>
            {
                u.Reachability = () => NetworkReachability.ReachableViaLocalAreaNetwork;
                u.Pace = ms => { };
            };
        }

        [TearDown]
        public void TearDown()
        {
            ResetAll();
            if (Directory.Exists(_root))
                Directory.Delete(_root, true);
        }

        private static ApmConfig Config(ApmBuildType buildType = ApmBuildType.Auto, ConsentState technical = ConsentState.Granted)
        {
            return new ApmConfig
            {
                ServerUrl = "http://apm.test",
                LogLevel = LogLevel.None,
                UploadInEditor = true,
                InitialUploadDelaySeconds = 3600f,
                FlushIntervalSeconds = 3600f,
                BuildType = buildType,
                DefaultTechnicalConsent = technical,
                SessionFilesUploadKey = "upload-key",
                SessionFilePartBytes = 64 * 1024,
                SessionFilesBytesPerSecond = 0
            };
        }

        private static void RunUntil(ApmClient client, Func<bool> done, int timeoutMs = 20000)
        {
            var watch = System.Diagnostics.Stopwatch.StartNew();
            while (!done())
            {
                client.UpdateForTests();
                if (watch.ElapsedMilliseconds > timeoutMs)
                    Assert.Fail("timed out: " + string.Join("; ", ApmSessionFiles.GetUploads().Select(i => i.ToString())));
                Thread.Sleep(2);
            }
        }

        [Test]
        public void ReleasePlayer_AtRunTime_NothingIsCreated()
        {
            // The engine says: not the Editor, not a Development Build. Even a
            // config that claims Dev cannot open the gate.
            ApmClient.SessionFileEngineDevForTests = () => false;
            foreach (var buildType in new[] { ApmBuildType.Auto, ApmBuildType.Dev, ApmBuildType.Release })
            {
                ApmClient.ResetForTests();
                ApmClient.StoragePathForTests = Path.Combine(_root, "events");
                ApmClient.SessionFilesPathForTests = Queue;
                ApmClient.SessionFileTransportForTests = _server;
                ApmClient.SessionFileEngineDevForTests = () => false;
                ApmClient.Initialize(Config(buildType));
                string dir = SessionDirs.Make(_root, "rel_" + buildType, 1000);
                Assert.IsFalse(ApmSessionFiles.IsAvailable, buildType.ToString());
                Assert.IsNull(ApmSessionFiles.Upload(dir), buildType.ToString());
                Assert.IsEmpty(ApmSessionFiles.GetUploads());
                Assert.IsFalse(ApmClient.GetStats().SessionFilesEnabled);
                Assert.IsNull(ApmClient.Instance.SessionFilesForTests, "no uploader, no thread");
                for (int i = 0; i < 10; i++)
                    ApmClient.Instance.UpdateForTests();
            }
            Assert.IsFalse(Directory.Exists(Queue), "no file written");
            Assert.AreEqual(0, _server.Requests.Count, "no request");
        }

        [Test]
        public void ReleaseBuildType_InTheEditor_NothingIsCreated()
        {
            ApmClient.Initialize(Config(ApmBuildType.Release));
            Assert.IsNull(ApmSessionFiles.Upload(SessionDirs.Make(_root, "rel", 0)));
            Assert.IsFalse(ApmSessionFiles.IsAvailable);
            Assert.IsFalse(Directory.Exists(Queue));
        }

        [Test]
        public void Disabled_NothingIsCreated()
        {
            var off = Config();
            off.EnableSessionFileUpload = false;
            ApmClient.Initialize(off);
            Assert.IsNull(ApmSessionFiles.Upload(SessionDirs.Make(_root, "off", 0)));
            ResetAll();
            SetUp();
            var noUpload = Config();
            noUpload.UploadInEditor = false;
            ApmClient.Initialize(noUpload);
            Assert.IsNull(ApmSessionFiles.Upload(SessionDirs.Make(_root, "off2", 0)), "Editor without UploadInEditor");
        }

        [Test]
        public void Editor_UploadCompletes()
        {
            ApmClient.Initialize(Config());
            var client = ApmClient.Instance;
            ApmClient.SetContext("gitCommit", "abcdef1234");
            string dir = SessionDirs.Make(_root, "2026-10-20_153042", 150 * 1024);
            string id = ApmSessionFiles.Upload(dir);
            Assert.IsNotNull(id);
            Assert.IsTrue(ApmSessionFiles.IsAvailable);
            RunUntil(client, () => ApmSessionFiles.GetUploads().Any(u => u.UploadId == id && u.State == SessionUploadState.Done));
            string create = _server.Created[id];
            StringAssert.Contains("\"build_type\":\"editor\"", create);
            StringAssert.Contains("\"git_commit\":\"abcdef1234\"", create, "the context key");
            StringAssert.Contains("\"device_id\":\"" + ApmClient.GetDeviceId() + "\"", create);
            var stats = ApmClient.GetStats();
            Assert.IsTrue(stats.SessionFilesEnabled);
            Assert.AreEqual(1, stats.SessionFilesCompleted);
            Assert.AreEqual(0, stats.SessionFilesPending);
            Assert.AreEqual(_server.Complete[id].Length, stats.SessionFileBytesSent);
            Assert.IsNotNull(ApmClient.GetSessionId());
        }

        [Test]
        public void ConsentDenied_DeletesTheQueue_AndRefuses()
        {
            ApmClient.Initialize(Config(technical: ConsentState.Undecided));
            var client = ApmClient.Instance;
            string id = ApmSessionFiles.Upload(SessionDirs.Make(_root, "s_c", 50 * 1024));
            Assert.IsNotNull(id, "Undecided: queued, not sent");
            RunUntil(client, () => File.Exists(Path.Combine(Queue, id + ".state")));
            ApmClient.SetConsent(ConsentCategory.Technical, ConsentState.Denied);
            RunUntil(client, () => Directory.GetFiles(Queue).Length == 0);
            Assert.IsNull(ApmSessionFiles.Upload(SessionDirs.Make(_root, "s_c2", 0)));
            Assert.AreEqual(0, _server.Requests.Count);
        }

        [Test]
        public void ResetDeviceId_DeletesTheQueue()
        {
            ApmClient.Initialize(Config(technical: ConsentState.Undecided));
            var client = ApmClient.Instance;
            string id = ApmSessionFiles.Upload(SessionDirs.Make(_root, "s_r", 50 * 1024));
            RunUntil(client, () => File.Exists(Path.Combine(Queue, id + ".state")));
            ApmClient.ResetDeviceId();
            RunUntil(client, () => Directory.GetFiles(Queue).Length == 0);
        }

        [Test]
        public void IdleUploader_UpdateDoesNotAllocate()
        {
            ApmClient.Initialize(Config());
            var uploader = ApmClient.Instance.SessionFilesForTests;
            Assert.IsNotNull(uploader);
            float t = 1f;
            uploader.Update(t, false);
            Assert.That(() =>
            {
                for (int i = 0; i < 100; i++)
                {
                    t += 0.016f;
                    uploader.Update(t, false);
                }
            }, Is.Not.AllocatingGCMemory());
        }
    }
}
