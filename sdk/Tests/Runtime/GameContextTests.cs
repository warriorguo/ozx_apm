using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text;
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
    /// APM-5: game context API, limits, snapshot at enqueue time, persistence,
    /// serialization and allocation behaviour.
    /// </summary>
    [TestFixture]
    public class GameContextTests
    {
        private string _dir;
        private ApmConfig _config;
        private OfflineStorage _store;
        private CultureInfo _culture;

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            PerfSamplePool.ClearForTests();
            _culture = Thread.CurrentThread.CurrentCulture;
            _dir = Path.Combine(Path.GetTempPath(), "ozx_apm_ctx_" + Guid.NewGuid().ToString("N"));
            _config = new ApmConfig
            {
                BatchSize = 20,
                FlushIntervalSeconds = 3600f,
                LowValueUploadIntervalSeconds = 0f,
                EnableCompression = false,
                LogLevel = LogLevel.None
            };
        }

        [TearDown]
        public void TearDown()
        {
            Thread.CurrentThread.CurrentCulture = _culture;
            _store?.Shutdown(0);
            _store = null;
            GameContext.ResetForTests();
            PerfSamplePool.ClearForTests();
            if (Directory.Exists(_dir))
                Directory.Delete(_dir, true);
        }

        private OfflineStorage Store()
        {
            return _store ?? (_store = new OfflineStorage(_config, _dir, false));
        }

        private static PerfSampleEvent Perf(float fps)
        {
            var e = new PerfSampleEvent
            {
                fps = fps,
                timestamp = 1700000000000,
                app_version = "1.0.0",
                platform = "Android",
                session_id = "s",
                device_id = "d"
            };
            ApmClient.StampContext(e, "release", "Adreno (TM) 642L", "8GB", "WiFi");
            return e;
        }

        private string AllStored()
        {
            return string.Concat(Directory.GetFiles(_dir)
                .Where(f => f.EndsWith(".seg", StringComparison.Ordinal) || f.EndsWith(".open", StringComparison.Ordinal))
                .OrderBy(f => f, StringComparer.Ordinal)
                .Select(File.ReadAllText));
        }

        private static bool IsJsonObject(string s)
        {
            var bytes = Encoding.UTF8.GetBytes(s);
            return JsonValidator.IsObject(bytes, 0, bytes.Length);
        }

        // ---- API semantics -------------------------------------------------

        [Test]
        public void SetGetRemoveClear()
        {
            Assert.IsTrue(ApmClient.SetContext("levelId", "3"));
            Assert.IsTrue(ApmClient.SetContext("roomId", "r-12"));
            Assert.AreEqual("3", ApmClient.GetContext("levelId"));
            Assert.AreEqual(2, GameContext.Count);

            Assert.IsTrue(ApmClient.SetContext("levelId", "4"));
            Assert.AreEqual("4", ApmClient.GetContext("levelId"));

            Assert.IsTrue(ApmClient.RemoveContext("levelId"));
            Assert.IsFalse(ApmClient.RemoveContext("levelId"));
            Assert.IsNull(ApmClient.GetContext("levelId"));

            // null and "" remove the key.
            Assert.IsTrue(ApmClient.SetContext("roomId", ""));
            Assert.IsNull(ApmClient.GetContext("roomId"));
            ApmClient.SetContext("a", "1");
            Assert.IsTrue(ApmClient.SetContext("a", null));
            Assert.AreEqual(0, GameContext.Count);

            ApmClient.SetContext("a", "1");
            ApmClient.SetContext("b", "2");
            ApmClient.ClearContext();
            Assert.AreEqual(0, GameContext.Count);
            Assert.AreSame(null, GameContext.Current.Json);
            Assert.IsNull(ApmClient.GetContext(null));
            Assert.IsFalse(ApmClient.RemoveContext(null));
        }

        [Test]
        public void WorksBeforeInitialize()
        {
            Assert.IsFalse(ApmClient.IsInitialized);
            Assert.IsTrue(ApmClient.SetContext("gitCommit", "abc123"));
            Assert.AreEqual("{\"gitCommit\":\"abc123\"}", GameContext.Current.Json);
            var stats = ApmClient.GetStats();
            Assert.AreEqual(1, stats.ContextKeys);
        }

        // ---- limits --------------------------------------------------------

        [Test]
        public void InvalidKeys_AreRejectedAndCounted_NeverThrow()
        {
            string[] bad = { null, "", "bad key", "k'ey", "a\"b", "a:b", "ключ", "a\nb", new string('k', 65) };
            foreach (var key in bad)
            {
                bool ok = true;
                Assert.DoesNotThrow(() => ok = ApmClient.SetContext(key, "v"), key);
                Assert.IsFalse(ok, key ?? "null");
            }
            Assert.AreEqual(bad.Length, GameContext.RejectedCount);
            Assert.AreEqual(0, GameContext.Count);
            Assert.AreEqual(bad.Length, ApmClient.GetStats().ContextRejected);

            foreach (var key in new[] { "a", "levelId", "room.id", "stage-type", "x_1", new string('k', 64) })
                Assert.IsTrue(ApmClient.SetContext(key, "v"), key);
        }

        [Test]
        public void LongValues_AreCut_NeverInsideASurrogatePair()
        {
            Assert.IsTrue(ApmClient.SetContext("weapons", new string('w', 300)));
            Assert.AreEqual(GameContext.MaxValueLength, ApmClient.GetContext("weapons").Length);
            Assert.AreEqual(1, GameContext.TruncatedCount);

            // An emoji (surrogate pair) straddling the cut is dropped as a whole.
            string value = new string('x', GameContext.MaxValueLength - 1) + "\U0001F3AE" + "tail";
            ApmClient.SetContext("emoji", value);
            string stored = ApmClient.GetContext("emoji");
            Assert.AreEqual(GameContext.MaxValueLength - 1, stored.Length);
            Assert.IsFalse(char.IsHighSurrogate(stored[stored.Length - 1]));
            Assert.AreEqual(2, GameContext.TruncatedCount);
        }

        [Test]
        public void AtMost64Keys()
        {
            for (int i = 0; i < GameContext.MaxKeys; i++)
                Assert.IsTrue(ApmClient.SetContext("k" + i, "v"));
            Assert.IsFalse(ApmClient.SetContext("one_too_many", "v"));
            Assert.AreEqual(GameContext.MaxKeys, GameContext.Count);
            // Updating and removing existing keys still works at the limit.
            Assert.IsTrue(ApmClient.SetContext("k0", "changed"));
            Assert.IsTrue(ApmClient.RemoveContext("k1"));
            Assert.IsTrue(ApmClient.SetContext("one_more", "v"));
        }

        [Test]
        public void TotalSizeIsCapped_AndARefusedUpdateRemovesTheStaleValue()
        {
            // 15 keys of ~266 characters each = ~3990 of 4096.
            for (int i = 0; i < 15; i++)
                Assert.IsTrue(ApmClient.SetContext("k" + i.ToString("00", CultureInfo.InvariantCulture), new string('v', 256)));
            Assert.IsFalse(ApmClient.SetContext("extra", new string('v', 200)));
            Assert.IsNull(ApmClient.GetContext("extra"));

            Assert.IsTrue(ApmClient.SetContext("small", "s"));
            Assert.IsFalse(ApmClient.SetContext("small", new string('v', 256)));
            Assert.IsNull(ApmClient.GetContext("small"), "a refused update must not leave the old value");
            Assert.LessOrEqual(GameContext.Current.Json.Length, GameContext.MaxTotalLength + 2);
        }

        // ---- snapshots -----------------------------------------------------

        [Test]
        public void Snapshot_IsImmutable_AndSharedUntilTheContextChanges()
        {
            ApmClient.SetContext("levelId", "1");
            var first = GameContext.Current;
            Assert.AreSame(first, GameContext.Current);

            // Same value again: no new snapshot.
            ApmClient.SetContext("levelId", "1");
            Assert.AreSame(first, GameContext.Current);

            ApmClient.SetContext("levelId", "2");
            var second = GameContext.Current;
            Assert.AreNotSame(first, second);
            Assert.IsTrue(first.TryGetValue("levelId", out string v1));
            Assert.AreEqual("1", v1);
            Assert.AreEqual("{\"levelId\":\"1\"}", first.Json);
            Assert.AreEqual("{\"levelId\":\"2\"}", second.Json);
        }

        [Test]
        public void EachEventCarriesTheContextOfItsEnqueueTime()
        {
            var before = Perf(60); // empty context
            ApmClient.SetContext("roomId", "r1");
            ApmClient.SetContext("levelId", "3");
            var inRoom1 = Perf(50);
            ApmClient.SetContext("roomId", "r2");
            var inRoom2 = Perf(40);
            ApmClient.ClearContext();
            var after = Perf(30);

            string a = JsonSerializer.SerializeEvent(before);
            string b = JsonSerializer.SerializeEvent(inRoom1);
            string c = JsonSerializer.SerializeEvent(inRoom2);
            string d = JsonSerializer.SerializeEvent(after);
            StringAssert.DoesNotContain("\"ctx\"", a);
            StringAssert.EndsWith(",\"ctx\":{\"levelId\":\"3\",\"roomId\":\"r1\"}}", b);
            StringAssert.EndsWith(",\"ctx\":{\"levelId\":\"3\",\"roomId\":\"r2\"}}", c);
            StringAssert.DoesNotContain("\"ctx\"", d);
            foreach (var json in new[] { a, b, c, d })
                Assert.IsTrue(IsJsonObject(json), json);
        }

        [Test]
        public void CollectorSuppliedContext_IsKept()
        {
            ApmClient.SetContext("roomId", "first");
            var early = GameContext.Current;
            ApmClient.SetContext("roomId", "later");
            var evt = new ExceptionEvent { ctx = early };
            ApmClient.StampContext(evt, "release", "", "", "");
            StringAssert.Contains("\"ctx\":{\"roomId\":\"first\"}", JsonSerializer.SerializeEvent(evt));
        }

        [Test]
        public void DeviceFields_AreSerialized()
        {
            string json = JsonSerializer.SerializeEvent(Perf(60));
            StringAssert.Contains("\"build_type\":\"release\"", json);
            StringAssert.Contains("\"gpu\":\"Adreno (TM) 642L\"", json);
            StringAssert.Contains("\"ram_class\":\"8GB\"", json);
            StringAssert.Contains("\"net_type\":\"WiFi\"", json);

            var bare = new PerfSampleEvent { timestamp = 1 };
            StringAssert.DoesNotContain("build_type", JsonSerializer.SerializeEvent(bare));
        }

        // ---- serialization -------------------------------------------------

        [Test]
        public void Values_AreEscaped()
        {
            string tricky = "it's a \"quote\", a back\\slash, a\nnewline, a tab\t, \u0001, ünïcødé 中文 🎮";
            ApmClient.SetContext("tricky", tricky);
            ApmClient.SetContext("fakeField", "x\",\"fps\":0,\"y\":\"");
            string json = JsonSerializer.SerializeEvent(Perf(60));
            Assert.IsTrue(IsJsonObject(json), json);
            StringAssert.Contains("\"tricky\":\"it's a \\\"quote\\\", a back\\\\slash, a\\nnewline, a tab\\t, \\u0001, ünïcødé 中文 🎮\"", json);
            StringAssert.Contains("\"fps\":60.00", json);
            // One line per event on disk.
            StringAssert.DoesNotContain("\n", json);
        }

        [Test]
        public void Serialization_IgnoresTheCurrentCulture()
        {
            ApmClient.SetContext("Ilevel", "1,5");
            ApmClient.SetContext("ilevel", "x");
            string invariant = JsonSerializer.SerializeEvent(Perf(59.5f));
            foreach (var name in new[] { "de-DE", "tr-TR", "ar-SA" })
            {
                Thread.CurrentThread.CurrentCulture = new CultureInfo(name);
                GameContext.Clear();
                ApmClient.SetContext("ilevel", "x");
                ApmClient.SetContext("Ilevel", "1,5");
                string json = JsonSerializer.SerializeEvent(Perf(59.5f));
                Assert.AreEqual(invariant, json, name);
                StringAssert.Contains("\"fps\":59.50", json);
                // Ordinal key order: 'I' (0x49) before 'i' (0x69) in every culture.
                StringAssert.EndsWith("\"ctx\":{\"Ilevel\":\"1,5\",\"ilevel\":\"x\"}}", json);
            }
        }

        // ---- persistence ---------------------------------------------------

        [Test]
        public void Context_IsPersistedWithTheEvent_AndSurvivesARestart()
        {
            ApmClient.SetContext("levelId", "7");
            ApmClient.SetContext("masterSeed", "123456789");
            var store = Store();
            store.Enqueue(Perf(60));
            ApmClient.SetContext("levelId", "8"); // after enqueue: must not leak into the stored event
            store.Pump();
            StringAssert.Contains("\"ctx\":{\"levelId\":\"7\",\"masterSeed\":\"123456789\"}", AllStored());
            StringAssert.DoesNotContain("\"levelId\":\"8\"", AllStored());

            // "Restart": a new instance on the same directory uploads the stored line as is.
            store.Shutdown(0);
            _store = null;
            GameContext.ResetForTests();
            var restarted = Store();
            restarted.RequestUpload();
            restarted.Pump();
            Assert.AreEqual(UploadPoll.Ready, restarted.PollUpload(out var batch));
            string body = Encoding.UTF8.GetString(batch.Payload);
            Assert.IsTrue(IsJsonObject(body), body);
            StringAssert.Contains("\"ctx\":{\"levelId\":\"7\",\"masterSeed\":\"123456789\"}", body);
        }

        [Test]
        public void AContextValueLookingLikeAType_DoesNotMakeASegmentHighValue()
        {
            byte[] exceptionMarker = OfflineStorage.HighValueMarkers[0];
            ApmClient.SetContext("type", "exception");
            string perfLine = JsonSerializer.SerializeEvent(Perf(60)) + "\n";
            var perf = Encoding.UTF8.GetBytes(perfLine + perfLine);
            StringAssert.Contains("\"type\":\"exception\"", perfLine);
            Assert.IsFalse(OfflineStorage.StartsAnyLine(perf, perf.Length, exceptionMarker));

            var exc = Encoding.UTF8.GetBytes(perfLine + JsonSerializer.SerializeEvent(new ExceptionEvent { fingerprint = "f" }) + "\n");
            Assert.IsTrue(OfflineStorage.StartsAnyLine(exc, exc.Length, exceptionMarker));
        }

        // ---- cost ----------------------------------------------------------

        // Fields, not literals: Mono allocates a string literal the first time each
        // method executes it, which would show up as an allocation of the delegate
        // under test (EnqueueEvent passes fields too).
        private static readonly string s_release = "release";
        private static readonly string s_gpu = "gpu";
        private static readonly string s_ram = "8GB";
        private static readonly string s_net = "WiFi";

        [Test]
        public void SteadyState_PerfSamplePath_DoesNotAllocate()
        {
            // A realistic context (OZX sets ~30 keys).
            for (int i = 0; i < 30; i++)
                ApmClient.SetContext("key" + i, "value-" + i);
            var store = Store();
            for (int i = 0; i < 4; i++) // warm up: pool, queue, snapshot
            {
                var w = PerfSamplePool.Rent();
                ApmClient.StampContext(w, s_release, s_gpu, s_ram, s_net);
                store.Enqueue(w);
                store.Pump();
            }
            // Spare events in the pool: the delegates below rent without the
            // storage worker returning them in between.
            var spare = new List<PerfSampleEvent>();
            for (int i = 0; i < 8; i++)
                spare.Add(PerfSamplePool.Rent());
            foreach (var e in spare)
                PerfSamplePool.Return(e);

            // The per-second perf sample: rent, stamp (build type, device, context
            // snapshot), enqueue. Measured on its first execution.
            Assert.That(() =>
            {
                var e = PerfSamplePool.Rent();
                e.fps = 60;
                e.timestamp = 1;
                ApmClient.StampContext(e, s_release, s_gpu, s_ram, s_net);
                store.Enqueue(e);
            }, Is.Not.AllocatingGCMemory(), "rent + stamp + enqueue");

            // Reading the unchanged snapshot, and setting a key to the value it
            // already has, are free too.
            string key = "key5", value = "value-5";
            Assert.That(() => { var unused = GameContext.Current; }, Is.Not.AllocatingGCMemory(), "GameContext.Current");
            Assert.That(() => { ApmClient.SetContext(key, value); }, Is.Not.AllocatingGCMemory(), "SetContext with the same value");
        }

        [Test]
        public void ReadingTheSnapshot_IsCheap()
        {
            for (int i = 0; i < 30; i++)
                ApmClient.SetContext("key" + i, "value-" + i);
            var unused = GameContext.Current;
            const int n = 1000000;
            var sw = System.Diagnostics.Stopwatch.StartNew();
            for (int i = 0; i < n; i++)
                unused = GameContext.Current;
            sw.Stop();
            double nsPerRead = sw.Elapsed.TotalMilliseconds * 1e6 / n;
            Debug.Log($"[OzxApm.Tests] GameContext.Current: {nsPerRead:F1} ns per read (unchanged context)");
            Assert.Less(nsPerRead, 1000, "reading an unchanged snapshot must stay trivial");
        }

        // ---- threads -------------------------------------------------------

        [Test]
        public void ConcurrentWritersAndReaders_SeeConsistentSnapshots()
        {
            var errors = new List<string>();
            var stop = new ManualResetEvent(false);
            var writers = new Thread[4];
            for (int t = 0; t < writers.Length; t++)
            {
                int id = t;
                writers[t] = new Thread(() =>
                {
                    try
                    {
                        for (int i = 0; i < 20000; i++)
                        {
                            GameContext.Set("w" + id, i.ToString(CultureInfo.InvariantCulture));
                            if (i % 100 == 0)
                                GameContext.Remove("w" + id);
                        }
                    }
                    catch (Exception ex)
                    {
                        lock (errors) errors.Add(ex.ToString());
                    }
                });
                writers[t].Start();
            }
            int reads = 0;
            while (writers.Any(w => w.IsAlive))
            {
                var snapshot = GameContext.Current;
                if (snapshot.Json != null && !IsJsonObject(snapshot.Json))
                    errors.Add("invalid JSON: " + snapshot.Json);
                for (int i = 1; i < snapshot.Count; i++)
                {
                    if (string.CompareOrdinal(snapshot.GetKey(i - 1), snapshot.GetKey(i)) >= 0)
                        errors.Add("unsorted snapshot");
                }
                reads++;
            }
            foreach (var w in writers)
                w.Join();
            CollectionAssert.IsEmpty(errors);
            Assert.Greater(reads, 0);
            Assert.AreEqual(4, GameContext.Current.Count);
            Assert.AreEqual("19999", GameContext.Current.GetValue(0));
        }

        // ---- build type and device info -------------------------------------

        [Test]
        public void BuildType_DetectionAndOverride()
        {
            Assert.AreEqual("editor", DeviceInfo.BuildTypeName(ApmBuildType.Auto, true, true));
            Assert.AreEqual("dev", DeviceInfo.BuildTypeName(ApmBuildType.Auto, false, true));
            Assert.AreEqual("release", DeviceInfo.BuildTypeName(ApmBuildType.Auto, false, false));
            Assert.AreEqual("release", DeviceInfo.BuildTypeName(ApmBuildType.Release, true, true));
            Assert.AreEqual("dev", DeviceInfo.BuildTypeName(ApmBuildType.Dev, false, false));
            Assert.AreEqual("editor", DeviceInfo.BuildTypeName(ApmBuildType.Editor, false, false));
            Assert.AreEqual(ApmBuildType.Auto, new ApmConfig().BuildType);
            Assert.IsFalse(new ApmConfig().UploadInEditor);
        }

        [Test]
        public void RamClass_RoundsTheReportedSizeUp()
        {
            Assert.AreEqual("unknown", DeviceInfo.RamClassFromMB(0));
            Assert.AreEqual("1GB", DeviceInfo.RamClassFromMB(980));
            Assert.AreEqual("2GB", DeviceInfo.RamClassFromMB(1850));
            Assert.AreEqual("3GB", DeviceInfo.RamClassFromMB(2780));
            Assert.AreEqual("4GB", DeviceInfo.RamClassFromMB(3700));
            Assert.AreEqual("6GB", DeviceInfo.RamClassFromMB(5600));
            Assert.AreEqual("8GB", DeviceInfo.RamClassFromMB(7450));
            Assert.AreEqual("12GB", DeviceInfo.RamClassFromMB(11200));
            Assert.AreEqual("16GB", DeviceInfo.RamClassFromMB(15000));
            Assert.AreEqual("24GB+", DeviceInfo.RamClassFromMB(32768));
        }

        [Test]
        public void NetworkType_Names()
        {
            Assert.AreEqual("WiFi", DeviceInfo.NetworkTypeName(NetworkReachability.ReachableViaLocalAreaNetwork));
            Assert.AreEqual("Cellular", DeviceInfo.NetworkTypeName(NetworkReachability.ReachableViaCarrierDataNetwork));
            Assert.AreEqual("None", DeviceInfo.NetworkTypeName(NetworkReachability.NotReachable));
        }
    }
}
