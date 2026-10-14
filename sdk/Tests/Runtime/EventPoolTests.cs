using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;
using NUnit.Framework;
using UnityEngine.TestTools.Constraints;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    [TestFixture]
    public class EventPoolTests
    {
        private string _dir;
        private ApmConfig _config;
        private OfflineStorage _store;

        [SetUp]
        public void SetUp()
        {
            PerfSamplePool.ClearForTests();
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_pool_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig { BatchSize = 20, FlushIntervalSeconds = 3600f, LogLevel = LogLevel.None };
            _store = new OfflineStorage(_config, _dir, false);
        }

        [TearDown]
        public void TearDown()
        {
            OfflineStorage.BeforeWriteHook = null;
            _store.Shutdown(0);
            PerfSamplePool.ClearForTests();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private static PerfSampleEvent Rent(float fps)
        {
            var e = PerfSamplePool.Rent();
            e.fps = fps;
            e.timestamp = 1700000000000;
            e.app_version = "1";
            e.platform = "Android";
            e.session_id = "s";
            e.device_id = "d";
            return e;
        }

        private string AllStored()
        {
            return string.Concat(Directory.GetFiles(_dir).Where(f => f.EndsWith(".seg") || f.EndsWith(".open")).Select(File.ReadAllText));
        }

        [Test]
        public void RentedEvent_IsReturnedAfterItIsPersisted_AndComesBackReset()
        {
            var first = Rent(60);
            _store.Enqueue(first);
            _store.Pump();
            Assert.AreEqual(1, PerfSamplePool.CountForTests, "returned once on disk");

            var second = PerfSamplePool.Rent();
            Assert.AreSame(first, second);
            Assert.IsNull(second.event_id);
            Assert.AreEqual(0, second.seq);
            Assert.AreEqual(0, second.timestamp);
            Assert.AreEqual(0f, second.fps);
            Assert.AreEqual("perf_sample", second.type);
        }

        [Test]
        public void ReusedInstances_GetFreshEventIdAndSeq()
        {
            for (int i = 0; i < 30; i++)
            {
                _store.Enqueue(Rent(i));
                _store.Pump(); // persisted and returned every time, so the same instance is reused
            }

            string all = AllStored();
            var ids = Regex.Matches(all, "\"event_id\":\"([0-9a-f]{32})\"").Cast<Match>().Select(m => m.Groups[1].Value).ToList();
            var seqs = Regex.Matches(all, "\"seq\":(\\d+)").Cast<Match>().Select(m => m.Groups[1].Value).ToList();
            Assert.AreEqual(30, ids.Count);
            Assert.AreEqual(30, ids.Distinct().Count());
            Assert.AreEqual(30, seqs.Distinct().Count());
            for (int i = 0; i < 30; i++)
                StringAssert.Contains("\"fps\":" + i + ".00", all);
        }

        [Test]
        public void EventsNotFromThePool_AreNeverReturned()
        {
            var own = new PerfSampleEvent { fps = 1, event_id = "mine" };
            _store.Enqueue(own);
            _store.Pump();

            Assert.AreEqual(0, PerfSamplePool.CountForTests);
            Assert.AreEqual("mine", own.event_id, "caller's object untouched");
        }

        [Test]
        public void FailedWrite_KeepsTheEvent_UntilItIsOnDisk()
        {
            OfflineStorage.BeforeWriteHook = _ => throw new IOException("Disk full");
            _store.Enqueue(Rent(1));
            _store.Pump();
            Assert.AreEqual(0, PerfSamplePool.CountForTests, "still queued for retry: must not be reused");

            OfflineStorage.BeforeWriteHook = null;
            _store.ResetWriteBackoffForTests();
            _store.Pump();
            Assert.AreEqual(1, PerfSamplePool.CountForTests);
            StringAssert.Contains("\"fps\":1.00", AllStored());
        }

        [Test]
        public void EmittingASample_DoesNotAllocate()
        {
            // Warm up: fill the pool and grow the queue once.
            for (int i = 0; i < 4; i++)
            {
                _store.Enqueue(Rent(i));
                _store.Pump();
            }

            Assert.That(() =>
            {
                var e = PerfSamplePool.Rent();
                e.fps = 60;
                e.timestamp = 1;
                _store.Enqueue(e);
            }, Is.Not.AllocatingGCMemory());
        }
    }
}
