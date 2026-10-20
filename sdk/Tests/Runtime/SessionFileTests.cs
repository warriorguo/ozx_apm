using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>Helpers: a fake AiTelemetry session directory.</summary>
    internal static class SessionDirs
    {
        public const string SessionJson = "{\"startedAt\":\"2026-10-20T15:30:42.1230000Z\",\"scene\":\"Gameplay\",\"unityVersion\":\"2022.3.62f3\"," +
            "\"gitCommit\":\"6116e2e4ff38de10eac61ba3c425e8f50a3aacdb\",\"dataSource\":\"builtin\",\"dataPackVersion\":0,\"dataPackSha256\":\"\"," +
            "\"sampleHz\":10,\"timeBase\":\"Time.time\",\"maxRetainedSessions\":20,\"apmSessionId\":\"1761000000000_a10a10a1\"}";

        /// <summary>
        /// SessionFolder/&lt;name&gt;/ with session.json, level.json, the channel
        /// files, a room directory and fillBytes of pseudo-random JSONL in rail.jsonl.
        /// Returns the directory's path.
        /// </summary>
        public static string Make(string parent, string name, int fillBytes, int seed = 1)
        {
            string dir = Path.Combine(parent, name);
            Directory.CreateDirectory(Path.Combine(dir, "room01_f0_room_1_0"));
            File.WriteAllText(Path.Combine(dir, "session.json"), SessionJson);
            File.WriteAllText(Path.Combine(dir, "level.json"), "{\"dataType\":\"LevelData\",\"generation\":{\"source\":\"session\",\"planId\":\"ch3\",\"masterSeed\":42}}");
            File.WriteAllText(Path.Combine(dir, "playermovement.jsonl"),
                "{\"t\":1.2500,\"frame\":76,\"run\":1,\"ev\":\"room_enter\",\"pos\":{\"x\":5.000,\"y\":0.660},\"roomId\":\"f0_room_1_0\"}\n" +
                "{\"t\":30.0833,\"frame\":1767,\"run\":1,\"ev\":\"died\",\"damageType\":\"Physical\",\"hit0Amount\":49.032,\"hit0Src\":\"quick_sand_worm\"," +
                "\"hit0SrcInstanceId\":80,\"hit0T\":29.6000,\"hit0Type\":\"Physical\",\"hitDirection\":{\"x\":0.000,\"y\":-1.000},\"hitPosition\":{\"x\":1.700,\"y\":8.500}," +
                "\"killerId\":\"quick_sand_worm\",\"killerInstanceId\":80,\"killerKind\":\"EnemyMelee\",\"killerTypeId\":\"quick_sand_worm\",\"liveEnemies\":7," +
                "\"pos\":{\"x\":1.703,\"y\":8.090},\"roomId\":\"f0_room_1_0\"}\n");
            File.WriteAllText(Path.Combine(dir, "encounter.jsonl"),
                "{\"t\":1.2500,\"frame\":76,\"run\":1,\"ev\":\"room_ready\",\"folder\":\"room01_f0_room_1_0\",\"isCave\":false,\"roomId\":\"f0_room_1_0\"}\n");
            File.WriteAllText(Path.Combine(dir, "runstart.jsonl"), "{\"t\":0.5000,\"frame\":31,\"run\":1,\"ev\":\"run\"}\n");
            File.WriteAllText(Path.Combine(dir, "room01_f0_room_1_0", "quicksandworm_80-wave1_event.jsonl"),
                "{\"t\":2.0000,\"frame\":120,\"run\":1,\"ev\":\"spawn\",\"instanceId\":80}\n");
            if (fillBytes > 0)
            {
                var rnd = new System.Random(seed);
                var bytes = new byte[96];
                using (var w = new StreamWriter(Path.Combine(dir, "rail.jsonl")))
                {
                    long written = 0;
                    int frame = 0;
                    while (written < fillBytes)
                    {
                        rnd.NextBytes(bytes);
                        string line = "{\"t\":" + (frame / 60.0).ToString("F4", System.Globalization.CultureInfo.InvariantCulture) + ",\"frame\":" + frame +
                            ",\"run\":1,\"ev\":\"train\",\"id\":\"" + Convert.ToBase64String(bytes) + "\"}";
                        w.Write(line);
                        w.Write('\n');
                        written += line.Length + 1;
                        frame++;
                    }
                }
            }
            return dir;
        }

        public static string Sha(byte[] data)
        {
            using (var sha = SHA256.Create())
                return SessionArchive.Hex(sha.ComputeHash(data));
        }

        /// <summary>Entry name -> content of a zip.</summary>
        public static Dictionary<string, byte[]> Unzip(byte[] zip)
        {
            var map = new Dictionary<string, byte[]>();
            using (var archive = new ZipArchive(new MemoryStream(zip), ZipArchiveMode.Read))
            {
                foreach (var e in archive.Entries)
                {
                    using (var s = e.Open())
                    using (var m = new MemoryStream())
                    {
                        s.CopyTo(m);
                        map[e.FullName] = m.ToArray();
                    }
                }
            }
            return map;
        }

        /// <summary>Relative path ('/') -> content of every file under dir, prefixed with the dir's name.</summary>
        public static Dictionary<string, byte[]> Files(string dir)
        {
            var map = new Dictionary<string, byte[]>();
            string name = Path.GetFileName(dir);
            foreach (var f in Directory.GetFiles(dir, "*", SearchOption.AllDirectories))
                map[name + "/" + f.Substring(dir.Length + 1).Replace('\\', '/')] = File.ReadAllBytes(f);
            return map;
        }
    }

    /// <summary>APM-13: the archive the device sends (zip of the session directory, SHA-256 while writing).</summary>
    [TestFixture]
    public class SessionArchiveTests
    {
        private string _root;

        [SetUp]
        public void SetUp()
        {
            _root = Path.Combine(Path.GetTempPath(), "ozx_apm_archive_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_root);
        }

        [TearDown]
        public void TearDown()
        {
            SessionArchive.ForceStoredForTests = false;
            if (Directory.Exists(_root))
                Directory.Delete(_root, true);
        }

        [Test]
        public void Archive_IsTheSessionDirectory_Deflate() => ArchiveIsTheSessionDirectory(false);

        [Test]
        public void Archive_IsTheSessionDirectory_Stored() => ArchiveIsTheSessionDirectory(true);

        private void ArchiveIsTheSessionDirectory(bool stored)
        {
            SessionArchive.ForceStoredForTests = stored;
            string dir = SessionDirs.Make(_root, "2026-10-20_153042", 200 * 1024);
            File.WriteAllText(Path.Combine(dir, "empty.jsonl"), "");
            File.WriteAllText(Path.Combine(dir, ".hidden"), "not archived");
            File.WriteAllText(Path.Combine(dir, "room01_f0_room_1_0", "café_1-wave1_event.jsonl"), "{\"ev\":\"x\"}\n");
            var files = SessionArchive.Snapshot(dir);
            string zipPath = Path.Combine(_root, "out.zip");
            var result = SessionArchive.Create(files, zipPath, new byte[SessionArchive.BufferSize], null);

            byte[] zip = File.ReadAllBytes(zipPath);
            Assert.AreEqual(zip.Length, result.Size);
            Assert.AreEqual(SessionDirs.Sha(zip), result.Sha256, "SHA-256 computed while writing");
            var expected = SessionDirs.Files(dir);
            expected.Remove("2026-10-20_153042/.hidden");
            var got = SessionDirs.Unzip(zip);
            CollectionAssert.AreEquivalent(expected.Keys, got.Keys);
            foreach (var kv in expected)
                CollectionAssert.AreEqual(kv.Value, got[kv.Key], kv.Key);
            Assert.AreEqual(expected.Count, result.FileCount);
            Assert.AreEqual(expected.Values.Sum(v => (long)v.Length), result.RawBytes);
            if (!stored && SessionArchive.DeflateBroken)
            {
                // Unity's Mono outside Unity has no zlib: the Stored fallback made the
                // (valid, checked above) archive. In the Editor and players Deflate works.
                Assert.IsFalse(result.Deflated);
                Assert.Ignore("no working DeflateStream on this runtime: Stored fallback verified");
            }
            Assert.AreEqual(!stored, result.Deflated);
            if (!stored)
                Assert.Less(result.Size, result.RawBytes, "JSONL compresses");
        }

        [Test]
        public void Snapshot_DetectsChanges()
        {
            string dir = SessionDirs.Make(_root, "s1", 0);
            var a = SessionArchive.Snapshot(dir);
            Assert.IsTrue(SessionArchive.SameSnapshot(a, SessionArchive.Snapshot(dir)));
            File.AppendAllText(Path.Combine(dir, "playermovement.jsonl"), "{}\n");
            Assert.IsFalse(SessionArchive.SameSnapshot(a, SessionArchive.Snapshot(dir)));
            var b = SessionArchive.Snapshot(dir);
            File.WriteAllText(Path.Combine(dir, "room01_f0_room_1_0", "new_1-wave1_event.jsonl"), "{}\n");
            Assert.IsFalse(SessionArchive.SameSnapshot(b, SessionArchive.Snapshot(dir)));
            // Entry names: "<dir>/...", sorted, '/' separators.
            var names = SessionArchive.Snapshot(dir).Select(f => f.Name).ToList();
            Assert.IsTrue(names.All(n => n.StartsWith("s1/")));
            CollectionAssert.AreEqual(names.OrderBy(n => n, StringComparer.Ordinal).ToList(), names);
            Assert.Contains("s1/room01_f0_room_1_0/new_1-wave1_event.jsonl", names);
        }

        [Test]
        public void Crc32_KnownValue()
        {
            var data = Encoding.ASCII.GetBytes("123456789");
            Assert.AreEqual(0xCBF43926u, ~Crc32.Update(0xFFFFFFFFu, data, 0, data.Length));
        }

        [Test]
        public void Create_IsCancellable()
        {
            string dir = SessionDirs.Make(_root, "s2", 300 * 1024);
            Assert.Throws<OperationCanceledException>(() =>
                SessionArchive.Create(SessionArchive.Snapshot(dir), Path.Combine(_root, "c.zip"), new byte[SessionArchive.BufferSize], () => true));
        }

        /// <summary>
        /// The server's fixture (server/internal/sessionfiles/testdata): an
        /// archive made by this code on Unity's Mono. Written only when
        /// OZX_APM13_FIXTURE names the output file.
        /// </summary>
        [Test]
        public void WriteServerFixture()
        {
            string output = Environment.GetEnvironmentVariable("OZX_APM13_FIXTURE");
            if (string.IsNullOrEmpty(output))
                Assert.Ignore("OZX_APM13_FIXTURE not set");
            string dir = SessionDirs.Make(_root, "2026-10-20_153042", 20 * 1024, 7);
            File.WriteAllText(Path.Combine(dir, "room01_f0_room_1_0", "café_1-wave1_event.jsonl"), "{\"t\":3.0000,\"frame\":180,\"run\":1,\"ev\":\"spawn\",\"instanceId\":1}\n");
            var r = SessionArchive.Create(SessionArchive.Snapshot(dir), output, new byte[SessionArchive.BufferSize], null);
            File.WriteAllText(output + ".sha256", r.Sha256 + "\n");
        }
    }

    /// <summary>
    /// An in-memory APM-13 server: create / parts / commit, the parts kept as
    /// sent (each checked against its SHA-256 header), the commit joining them.
    /// Requests complete on the next main-thread Update.
    /// </summary>
    internal sealed class FakeSessionServer : ISessionHttpTransport
    {
        public readonly object Lock = new object();
        public readonly List<SessionHttpJob> Requests = new List<SessionHttpJob>();
        public readonly Dictionary<string, string> Created = new Dictionary<string, string>(); // id -> create body
        public readonly Dictionary<string, Dictionary<int, byte[]>> Parts = new Dictionary<string, Dictionary<int, byte[]>>();
        public readonly Dictionary<string, byte[]> Complete = new Dictionary<string, byte[]>();
        /// <summary>Answers to use instead of the protocol's, first matching the request (method + path suffix), once each.</summary>
        public readonly List<KeyValuePair<Func<SessionHttpJob, bool>, Action<SessionHttpJob>>> Overrides =
            new List<KeyValuePair<Func<SessionHttpJob, bool>, Action<SessionHttpJob>>>();
        public int PartsAccepted;
        public int PartRequests;

        public static Action<SessionHttpJob> Answer(long status, string body, string retryAfter = null) => job =>
        {
            job.Status = status;
            job.ResponseText = body;
            job.RetryAfter = retryAfter;
        };

        public static Action<SessionHttpJob> Offline => job =>
        {
            job.Status = 0;
            job.TransportError = true;
            job.Error = "Cannot connect to destination host";
        };

        public void Override(Func<SessionHttpJob, bool> when, Action<SessionHttpJob> answer)
        {
            lock (Lock)
                Overrides.Add(new KeyValuePair<Func<SessionHttpJob, bool>, Action<SessionHttpJob>>(when, answer));
        }

        public ISessionHttpRequest Start(SessionHttpJob job)
        {
            lock (Lock)
            {
                Requests.Add(job);
                for (int i = 0; i < Overrides.Count; i++)
                {
                    if (Overrides[i].Key(job))
                    {
                        var answer = Overrides[i].Value;
                        Overrides.RemoveAt(i);
                        return new Done(answer);
                    }
                }
                return new Done(Handle);
            }
        }

        private static string Header(SessionHttpJob job, string name)
        {
            for (int i = 0; i + 1 < job.Headers.Length; i += 2)
            {
                if (job.Headers[i] == name)
                    return job.Headers[i + 1];
            }
            return null;
        }

        private static string Id(SessionHttpJob job)
        {
            string path = job.Url.Substring(job.Url.IndexOf("/v1/session-files", StringComparison.Ordinal) + "/v1/session-files".Length).TrimStart('/');
            int slash = path.IndexOf('/');
            return slash < 0 ? path : path.Substring(0, slash);
        }

        private void Handle(SessionHttpJob job)
        {
            lock (Lock)
            {
                if (Header(job, "X-Apm-Upload-Key") != "upload-key")
                {
                    Answer(401, "{\"error\":\"unauthorized: wrong upload key\"}")(job);
                    return;
                }
                if (job.Method == "POST" && job.Url.EndsWith("/v1/session-files"))
                {
                    string body = Encoding.UTF8.GetString(job.Body);
                    string id = SessionFileUploader.JsonField(body, "upload_id");
                    if (SessionFileUploader.JsonField(body, "build_type") == "release")
                    {
                        Answer(403, "{\"error\":\"release\",\"reject_code\":\"release_build\"}")(job);
                        return;
                    }
                    Created[id] = body;
                    if (Complete.ContainsKey(id))
                    {
                        Answer(200, "{\"upload_id\":\"" + id + "\",\"status\":\"complete\",\"received\":[]}")(job);
                        return;
                    }
                    if (!Parts.ContainsKey(id))
                        Parts[id] = new Dictionary<int, byte[]>();
                    string received = string.Join(",", Parts[id].Keys.OrderBy(k => k));
                    Answer(200, "{\"upload_id\":\"" + id + "\",\"status\":\"uploading\",\"received\":[" + received + "]}")(job);
                    return;
                }
                if (job.Method == "PUT")
                {
                    PartRequests++;
                    string id = Id(job);
                    int n = int.Parse(job.Url.Substring(job.Url.LastIndexOf('/') + 1));
                    if (!Parts.ContainsKey(id))
                    {
                        Answer(404, "{\"error\":\"no such upload\"}")(job);
                        return;
                    }
                    byte[] data = File.ReadAllBytes(job.BodyFile);
                    if (SessionDirs.Sha(data) != Header(job, "X-Apm-Part-Sha256"))
                    {
                        Answer(400, "{\"error\":\"sha mismatch\"}")(job);
                        return;
                    }
                    Parts[id][n] = data;
                    PartsAccepted++;
                    Answer(200, "{\"part\":" + n + ",\"received\":true}")(job);
                    return;
                }
                if (job.Method == "POST" && job.Url.EndsWith("/commit"))
                {
                    string id = Id(job);
                    string create = Created[id];
                    long size = long.Parse(SessionFileUploader.JsonIntArray("{\"a\":[" + JsonNumber(create, "size_bytes") + "]}", "a")[0].ToString());
                    int partBytes = int.Parse(JsonNumber(create, "part_bytes"));
                    int count = (int)((size + partBytes - 1) / partBytes);
                    var missing = Enumerable.Range(0, count).Where(i => !Parts[id].ContainsKey(i)).ToList();
                    if (missing.Count > 0)
                    {
                        Answer(409, "{\"error\":\"parts missing\",\"missing\":[" + string.Join(",", missing) + "]}")(job);
                        return;
                    }
                    var all = Enumerable.Range(0, count).SelectMany(i => Parts[id][i]).ToArray();
                    if (SessionDirs.Sha(all) != SessionFileUploader.JsonField(create, "sha256"))
                    {
                        Answer(422, "{\"error\":\"checksum\",\"reject_code\":\"checksum_mismatch\"}")(job);
                        return;
                    }
                    Complete[id] = all;
                    Answer(200, "{\"upload_id\":\"" + id + "\",\"status\":\"complete\"}")(job);
                    return;
                }
                Answer(404, "{}")(job);
            }
        }

        internal static string JsonNumber(string text, string key)
        {
            int i = text.IndexOf("\"" + key + "\":", StringComparison.Ordinal);
            if (i < 0)
                return null;
            i += key.Length + 3;
            int j = i;
            while (j < text.Length && (char.IsDigit(text[j]) || text[j] == '-'))
                j++;
            return text.Substring(i, j - i);
        }

        private sealed class Done : ISessionHttpRequest
        {
            private readonly Action<SessionHttpJob> _answer;
            public Done(Action<SessionHttpJob> answer) => _answer = answer;
            public bool IsDone => true;
            public void Complete(SessionHttpJob job) => _answer(job);
            public void Abort() { }
            public void Dispose() { }
        }
    }

    /// <summary>
    /// APM-13: the uploader's protocol, persistence and resume, failures,
    /// consent and network rules, against FakeSessionServer. The worker thread
    /// is real; the test thread plays the main thread.
    /// </summary>
    [TestFixture]
    public class SessionFileUploaderTests
    {
        private string _root;
        private string Queue => Path.Combine(_root, SessionFileUploader.DirectoryName);
        private readonly List<SessionFileUploader> _uploaders = new List<SessionFileUploader>();
        private volatile bool _allowed;
        private long _clockOffset;
        private NetworkReachability _reach;

        [SetUp]
        public void SetUp()
        {
            _root = Path.Combine(Path.GetTempPath(), "ozx_apm_sf_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(_root);
            _allowed = true;
            _clockOffset = 0;
            _reach = NetworkReachability.ReachableViaLocalAreaNetwork;
        }

        [TearDown]
        public void TearDown()
        {
            foreach (var u in _uploaders)
                u.Shutdown(2000);
            _uploaders.Clear();
            if (Directory.Exists(_root))
                Directory.Delete(_root, true);
        }

        private static ApmConfig Config() => new ApmConfig
        {
            ServerUrl = "http://apm.test",
            AppKey = "app-key",
            LogLevel = LogLevel.None,
            SessionFilesUploadKey = "upload-key",
            SessionFilePartBytes = 64 * 1024,
            SessionFilesBytesPerSecond = 0
        };

        private static SessionUploadFacts Facts() => new SessionUploadFacts
        {
            DeviceId = "0123456789abcdef0123456789abcdef", CurrentSessionId = "current_session", AppVersion = "1.4.0", Build = "140",
            BuildType = "dev", Platform = "Android", DeviceModel = "samsung SM-A336B", OsVersion = "Android OS 16", UnityVersion = "2022.3.62f3"
        };

        private SessionFileUploader NewUploader(FakeSessionServer server, ApmConfig config = null)
        {
            var u = new SessionFileUploader(config ?? Config(), Queue, new SessionFileHttp(server), () => _allowed);
            u.NowMs = () => DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() + Interlocked.Read(ref _clockOffset);
            u.Reachability = () => _reach;
            u.Pace = ms => { };
            _uploaders.Add(u);
            u.Start();
            return u;
        }

        /// <summary>Runs the main thread's part until the condition holds.</summary>
        private static void RunUntil(SessionFileUploader u, Func<bool> condition, int timeoutMs = 20000, bool othersSending = false)
        {
            var watch = System.Diagnostics.Stopwatch.StartNew();
            while (!condition())
            {
                u.Update((float)watch.Elapsed.TotalSeconds, othersSending);
                if (watch.ElapsedMilliseconds % 50 == 0)
                    u.WakeForTests(); // the worker re-reads the (test) clock
                if (watch.ElapsedMilliseconds > timeoutMs)
                    Assert.Fail("timed out; uploads: " + string.Join("; ", u.GetUploads().Select(i => i.ToString())));
                Thread.Sleep(1);
            }
        }

        private static void RunFor(SessionFileUploader u, int ms, bool othersSending = false)
        {
            var watch = System.Diagnostics.Stopwatch.StartNew();
            while (watch.ElapsedMilliseconds < ms)
            {
                u.Update((float)watch.Elapsed.TotalSeconds, othersSending);
                if (watch.ElapsedMilliseconds % 50 == 0)
                    u.WakeForTests(); // the worker re-reads the (test) clock
                Thread.Sleep(1);
            }
        }

        private static SessionUploadInfo Info(SessionFileUploader u, string id) => u.GetUploads().FirstOrDefault(i => i.UploadId == id);

        private static bool Is(SessionFileUploader u, string id, SessionUploadState state) => Info(u, id)?.State == state;

        [Test]
        public void Upload_SendsTheArchiveInParts()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "2026-10-20_153042", 300 * 1024);
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            Assert.IsNotNull(id);
            Assert.AreEqual(32, id.Length);
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));

            var got = SessionDirs.Unzip(server.Complete[id]);
            var want = SessionDirs.Files(dir);
            CollectionAssert.AreEquivalent(want.Keys, got.Keys);
            foreach (var kv in want)
                CollectionAssert.AreEqual(kv.Value, got[kv.Key], kv.Key);
            Assert.Greater(server.PartsAccepted, 1, "more than one part");
            Assert.AreEqual(server.PartsAccepted, server.PartRequests);
            // The create request: what the index needs.
            string create = server.Created[id];
            StringAssert.Contains("\"device_id\":\"0123456789abcdef0123456789abcdef\"", create);
            StringAssert.Contains("\"session_id\":\"1761000000000_a10a10a1\"", create, "session.json's apmSessionId");
            StringAssert.Contains("\"recording_name\":\"2026-10-20_153042\"", create);
            StringAssert.Contains("\"build_type\":\"dev\"", create);
            StringAssert.Contains("\"build\":\"140\"", create);
            StringAssert.Contains("\"session_json\":\"{\\\"startedAt\\\"", create);
            Assert.AreEqual(SessionDirs.Files(dir).Count.ToString(), FakeSessionServer.JsonNumber(create, "file_count"));
            // Headers: upload key, app key, SDK version, device id on parts.
            var part = server.Requests.First(r => r.Method == "PUT");
            CollectionAssert.IsSubsetOf(new[] { "X-Apm-Upload-Key", "upload-key", "X-App-Key", "app-key", "X-Apm-Device-Id" }, part.Headers);
            // The local copy is gone; the directory is untouched.
            Assert.IsEmpty(Directory.GetFiles(Queue));
            Assert.IsTrue(File.Exists(Path.Combine(dir, "session.json")));
            Assert.AreEqual(1, u.Completed);
            Assert.AreEqual(server.Complete[id].Length, u.BytesSent);
        }

        [Test]
        public void Resume_AfterAKill_SendsOnlyTheMissingParts()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "2026-10-20_153042", 400 * 1024);
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            RunUntil(u, () => server.PartsAccepted >= 2);
            u.Shutdown(5000); // the app is killed (the state is on disk)
            int sentBefore;
            lock (server.Lock)
                sentBefore = server.PartsAccepted;
            Assert.IsTrue(File.Exists(Path.Combine(Queue, id + ".state")));
            Assert.IsTrue(File.Exists(Path.Combine(Queue, id + ".zip")));
            string state = File.ReadAllText(Path.Combine(Queue, id + ".state"));
            StringAssert.Contains("state=Uploading", state);

            var u2 = NewUploader(server);
            RunUntil(u2, () => Is(u2, id, SessionUploadState.Done));
            int parts = server.Parts[id].Count;
            Assert.AreEqual(parts, server.PartRequests, "each part sent once (none of the received ones again)");
            Assert.Greater(parts, sentBefore);
            CollectionAssert.AreEquivalent(SessionDirs.Files(dir).Keys, SessionDirs.Unzip(server.Complete[id]).Keys);
            Assert.IsEmpty(Directory.GetFiles(Queue));
        }

        [Test]
        public void Offline_WaitsAndThenSends()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "s_offline", 100 * 1024);
            _reach = NetworkReachability.NotReachable;
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            RunUntil(u, () => Is(u, id, SessionUploadState.Packed), 10000);
            RunFor(u, 300);
            Assert.AreEqual(0, server.Requests.Count, "nothing is sent offline");
            _reach = NetworkReachability.ReachableViaLocalAreaNetwork;
            Interlocked.Add(ref _clockOffset, 10000);
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));
        }

        [Test]
        public void WifiOnly_WaitsOnMobileData()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "s_cell", 0);
            _reach = NetworkReachability.ReachableViaCarrierDataNetwork;
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            RunUntil(u, () => Is(u, id, SessionUploadState.Packed), 10000);
            RunFor(u, 300);
            Assert.AreEqual(0, server.Requests.Count);
            var cfg = Config();
            cfg.SessionFilesWifiOnly = false;
            u.Shutdown(2000);
            var u2 = NewUploader(server, cfg);
            RunUntil(u2, () => Is(u2, id, SessionUploadState.Done));
        }

        [Test]
        public void TransientFailures_AreRetried_RejectsDrop()
        {
            var server = new FakeSessionServer();
            server.Override(j => j.Method == "POST" && j.Url.EndsWith("/v1/session-files"), FakeSessionServer.Offline);
            server.Override(j => j.Method == "PUT", FakeSessionServer.Answer(503, "{\"error\":\"busy\",\"retry\":true}", "1"));
            server.Override(j => j.Method == "PUT", FakeSessionServer.Answer(400, "{\"error\":\"part 0: SHA-256 mismatch, send it again\"}"));
            string dir = SessionDirs.Make(_root, "s_retry", 100 * 1024);
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            var watch = System.Diagnostics.Stopwatch.StartNew();
            while (!Is(u, id, SessionUploadState.Done))
            {
                Interlocked.Add(ref _clockOffset, 5000); // the backoffs pass
                u.WakeForTests();
                RunFor(u, 20);
                Assert.Less(watch.ElapsedMilliseconds, 20000, "timed out: " + string.Join("; ", u.GetUploads().Select(i => i.ToString())) + " | " + u.LastError + " | " + string.Join(", ", server.Requests.Select(r => r.Method + " " + r.Url.Substring(r.Url.LastIndexOf('/')) + " " + r.Status)));
            }
            Assert.IsNotNull(server.Complete[id]);

            // A reject_code drops the upload and its copy.
            server.Override(j => j.Method == "POST" && j.Url.EndsWith("/v1/session-files"),
                FakeSessionServer.Answer(413, "{\"error\":\"too big\",\"reject_code\":\"too_large\"}"));
            string id2 = u.Enqueue(SessionDirs.Make(_root, "s_reject", 0), Facts());
            RunUntil(u, () => Is(u, id2, SessionUploadState.Failed));
            StringAssert.Contains("too_large", Info(u, id2).Error);
            Assert.IsEmpty(Directory.GetFiles(Queue));
            Assert.AreEqual(1, u.Failed);
        }

        [Test]
        public void ServerForgotTheUpload_CreatesItAgain()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "s_404", 150 * 1024);
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            RunUntil(u, () => server.PartsAccepted >= 1);
            lock (server.Lock)
            {
                // The server's cleaner removed the upload (days without progress).
                server.Parts.Remove(id);
                server.Created.Remove(id);
            }
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));
            Assert.GreaterOrEqual(server.Requests.Count(r => r.Method == "POST" && r.Url.EndsWith("/v1/session-files")), 2);
        }

        [Test]
        public void CommitWithMissingParts_SendsThemAgain()
        {
            var server = new FakeSessionServer();
            string dir = SessionDirs.Make(_root, "s_missing", 200 * 1024);
            var u = NewUploader(server);
            string id = u.Enqueue(dir, Facts());
            // Every part is acknowledged but part 1 is lost on the server.
            server.Override(j => j.Method == "PUT" && j.Url.EndsWith("/parts/1"), FakeSessionServer.Answer(200, "{\"part\":1,\"received\":true}"));
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));
            Assert.AreEqual(1, server.Requests.Count(r => r.Url.EndsWith("/commit") && r.Status == 409));
            Assert.AreEqual(2, server.Requests.Count(r => r.Url.EndsWith("/parts/1")));
        }

        [Test]
        public void NotASessionDirectory_IsDropped()
        {
            var server = new FakeSessionServer();
            var u = NewUploader(server);
            string notSession = Path.Combine(_root, "no_session_json");
            Directory.CreateDirectory(notSession);
            File.WriteAllText(Path.Combine(notSession, "x.jsonl"), "{}");
            string id = u.Enqueue(notSession, Facts());
            string id2 = u.Enqueue(Path.Combine(_root, "missing_dir"), Facts());
            RunUntil(u, () => Is(u, id, SessionUploadState.Failed) && Is(u, id2, SessionUploadState.Failed));
            StringAssert.Contains("session.json", Info(u, id).Error);
            Assert.IsNull(u.Enqueue(Path.Combine(_root, ".hidden"), Facts()), "a name that cannot be a session");
            Assert.IsNull(u.Enqueue(Path.Combine(_root, "a b"), Facts()));
            Assert.AreEqual(0, server.Requests.Count);
        }

        [Test]
        public void PendingLimit()
        {
            var server = new FakeSessionServer();
            var cfg = Config();
            cfg.MaxPendingSessionFiles = 2;
            _allowed = false; // nothing leaves the queue
            var u = NewUploader(server, cfg);
            string dir = SessionDirs.Make(_root, "s_limit", 0);
            Assert.IsNotNull(u.Enqueue(dir, Facts()));
            Assert.IsNotNull(u.Enqueue(dir, Facts()));
            Assert.IsNull(u.Enqueue(dir, Facts()));
        }

        [Test]
        public void NoConsent_NothingSent_ClearDeletesTheQueue()
        {
            var server = new FakeSessionServer();
            _allowed = false;
            var u = NewUploader(server);
            string id = u.Enqueue(SessionDirs.Make(_root, "s_consent", 50 * 1024), Facts());
            RunUntil(u, () => File.Exists(Path.Combine(Queue, id + ".state")));
            RunFor(u, 300);
            Assert.AreEqual(0, server.Requests.Count);
            u.Clear();
            RunUntil(u, () => Directory.GetFiles(Queue).Length == 0 && u.GetUploads().Length == 0);
            _allowed = true;
            u.OnConsent(ConsentState.Granted);
            RunFor(u, 300);
            Assert.AreEqual(0, server.Requests.Count, "a cleared upload is never sent");
        }

        [Test]
        public void ConsentGranted_ThenUploaded()
        {
            var server = new FakeSessionServer();
            _allowed = false;
            var u = NewUploader(server);
            string id = u.Enqueue(SessionDirs.Make(_root, "s_later", 50 * 1024), Facts());
            RunFor(u, 200);
            Assert.AreEqual(0, server.Requests.Count);
            _allowed = true;
            u.OnConsent(ConsentState.Granted);
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));
        }

        [Test]
        public void OthersSending_HoldsTheRequest()
        {
            var server = new FakeSessionServer();
            var u = NewUploader(server);
            string id = u.Enqueue(SessionDirs.Make(_root, "s_hold", 0), Facts());
            RunFor(u, 500, othersSending: true);
            Assert.AreEqual(0, server.Requests.Count, "held while the event uploader sends");
            RunUntil(u, () => Is(u, id, SessionUploadState.Done));
        }

        [Test]
        public void ReleaseBuildType_IsRefusedByTheServer()
        {
            var server = new FakeSessionServer();
            var u = NewUploader(server);
            var facts = Facts();
            facts.BuildType = "release";
            string id = u.Enqueue(SessionDirs.Make(_root, "s_rel", 0), facts);
            RunUntil(u, () => Is(u, id, SessionUploadState.Failed));
            StringAssert.Contains("release_build", Info(u, id).Error);
        }

        [Test]
        public void TooOld_IsDropped()
        {
            var server = new FakeSessionServer();
            _allowed = false;
            var u = NewUploader(server);
            string id = u.Enqueue(SessionDirs.Make(_root, "s_old", 0), Facts());
            RunUntil(u, () => File.Exists(Path.Combine(Queue, id + ".state")));
            Interlocked.Add(ref _clockOffset, 15L * 86400000L);
            _allowed = true;
            u.OnConsent(ConsentState.Granted);
            RunUntil(u, () => Is(u, id, SessionUploadState.Failed));
            StringAssert.Contains("14 days", Info(u, id).Error);
            Assert.AreEqual(0, server.Requests.Count);
        }

        [Test]
        public void Record_RoundTrips()
        {
            var r = new SessionFileRecord
            {
                Id = "abc", Directory = "/data/x/Session Folder/2026-10-20_153042", Recording = "2026-10-20_153042", State = SessionUploadState.Uploading,
                CreatedAtMs = 1761000000000, DeviceId = "d", SessionId = "s", GitCommit = "c\nwith=newline\\", Size = 123456789012, Sha256 = "ff",
                PartBytes = 524288, PartCount = 3, Acked = "101", FileCount = 9, RawBytes = 5, PackAttempts = 2, Error = null, CurrentSession = "cur"
            };
            var p = SessionFileRecord.Parse(r.Serialize());
            Assert.AreEqual(r.Serialize(), p.Serialize());
            Assert.AreEqual("c\nwith=newline\\", p.GitCommit);
            Assert.AreEqual(1, p.NextMissingPart());
            Assert.AreEqual(2, p.AckedCount);
            Assert.IsNull(SessionFileRecord.Parse("garbage"));
        }

        [Test]
        public void Create404_BacksOff()
        {
            var server = new FakeSessionServer();
            for (int i = 0; i < 50; i++)
                server.Override(j => j.Method == "POST" && j.Url.EndsWith("/v1/session-files"), FakeSessionServer.Answer(404, "404 page not found"));
            var u = NewUploader(server);
            string id = u.Enqueue(SessionDirs.Make(_root, "s_404c", 0), Facts());
            RunUntil(u, () => server.Requests.Count >= 1);
            RunFor(u, 500);
            Assert.LessOrEqual(server.Requests.Count, 2, "a 404 is retried after a backoff, not at once");
            Assert.AreEqual(SessionUploadState.Packed, Info(u, id).State);
        }

        [Test]
        public void HttpHandoff_ClosedOrAbandonedJobsNeverThrow()
        {
            var server = new FakeSessionServer();
            var http = new SessionFileHttp(server);
            var job = new SessionHttpJob { Method = "GET", Url = "http://apm.test/v1/session-files", Headers = new string[0] };
            // The worker gives up before the main thread runs: withdrawn, never started.
            Assert.IsFalse(http.Send(job, 10));
            http.Update(0f, false);
            Assert.AreEqual(0, server.Requests.Count);
            // Taken by the main thread after the worker disposed its event: no exception.
            var job2 = new SessionHttpJob { Method = "GET", Url = "http://apm.test/x", Headers = new string[0] };
            var t = new Thread(() => http.Send(job2, 2000));
            t.Start();
            while (!http.Busy)
                Thread.Sleep(1);
            job2.Done.Close();
            Assert.DoesNotThrow(() => http.Update(0f, false));
            Assert.DoesNotThrow(() => http.Close());
            t.Join();
        }

        [Test]
        public void EntryNames_TheServerRefuses_FailEarly()
        {
            var ok = new List<SessionArchive.FileEntry> { new SessionArchive.FileEntry { Name = "s/a.jsonl" } };
            Assert.IsNull(SessionFileUploader.FirstInvalidEntryName(ok));
            ok.Add(new SessionArchive.FileEntry { Name = "s/c:x" });
            Assert.AreEqual("s/c:x", SessionFileUploader.FirstInvalidEntryName(ok));
            Assert.IsNotNull(SessionFileUploader.FirstInvalidEntryName(new List<SessionArchive.FileEntry> { new SessionArchive.FileEntry { Name = "s/" + new string('a', 600) } }));
        }

        [Test]
        public void JsonHelpers()
        {
            Assert.AreEqual("uploading", SessionFileUploader.JsonField("{\"a\":1, \"status\" : \"uploading\"}", "status"));
            Assert.AreEqual("x\"y", SessionFileUploader.JsonField("{\"error\":\"x\\\"y\"}", "error"));
            Assert.IsNull(SessionFileUploader.JsonField("{\"reject_code\":null}", "reject_code"));
            CollectionAssert.AreEqual(new[] { 0, 2, 13 }, SessionFileUploader.JsonIntArray("{\"received\":[0, 2,13]}", "received"));
            CollectionAssert.IsEmpty(SessionFileUploader.JsonIntArray("{\"received\":[]}", "received"));
            Assert.IsNull(SessionFileUploader.JsonIntArray("{}", "received"));
            Assert.IsTrue(SessionFileUploader.IsValidRecordingName("2026-08-11_153042_2"));
            Assert.IsFalse(SessionFileUploader.IsValidRecordingName(".."));
            Assert.IsFalse(SessionFileUploader.IsValidRecordingName("-x"));
            Assert.IsFalse(SessionFileUploader.IsValidId("a/b"));
        }
    }

    /// <summary>
    /// APM-13: the build-type gate. Release players never upload: the code is
    /// compiled out (no DEVELOPMENT_BUILD, no UNITY_EDITOR) and, where it is
    /// compiled in, the engine and the SDK's build type must say dev/editor.
    /// </summary>
    [TestFixture]
    public class SessionFileGateTests
    {
        [Test]
        public void Gate_RefusesReleaseBuilds()
        {
            Assert.IsFalse(ApmSessionFiles.Allowed("release", true, true), "build type release");
            Assert.IsFalse(ApmSessionFiles.Allowed("release", false, false));
            Assert.IsFalse(ApmSessionFiles.Allowed("dev", false, false), "a release player configured as Dev");
            Assert.IsFalse(ApmSessionFiles.Allowed("editor", false, false));
            Assert.IsFalse(ApmSessionFiles.Allowed("", true, true));
            Assert.IsFalse(ApmSessionFiles.Allowed(null, true, true));
#if UNITY_EDITOR || DEVELOPMENT_BUILD
            Assert.IsTrue(ApmSessionFiles.CompiledIn);
            Assert.IsTrue(ApmSessionFiles.Allowed("dev", false, true), "a Development Build");
            Assert.IsTrue(ApmSessionFiles.Allowed("editor", true, true), "the Editor");
#endif
        }

#if !UNITY_EDITOR && !DEVELOPMENT_BUILD
        /// <summary>
        /// Compiled as a release player is (this runs on Unity's Mono outside
        /// Unity, without UNITY_EDITOR and DEVELOPMENT_BUILD): nothing can open
        /// the gate, Upload is a no-op that creates nothing.
        /// </summary>
        [Test]
        public void ReleasePlayer_UploadIsCompiledOut()
        {
            Assert.IsFalse(ApmSessionFiles.CompiledIn);
            Assert.IsFalse(ApmSessionFiles.Allowed("dev", true, true));
            Assert.IsFalse(ApmSessionFiles.Allowed("editor", true, true));
            string dir = Path.Combine(Path.GetTempPath(), "ozx_apm_release_" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(dir);
            File.WriteAllText(Path.Combine(dir, "session.json"), "{}");
            try
            {
                Assert.IsNull(ApmSessionFiles.Upload(dir));
                Assert.IsNull(ApmSessionFiles.Upload(dir, new SessionUploadOptions { GitCommit = "abc" }));
                Assert.IsEmpty(ApmSessionFiles.GetUploads());
                Assert.IsFalse(ApmSessionFiles.IsAvailable);
            }
            finally
            {
                Directory.Delete(dir, true);
            }
        }
#endif
    }
}
