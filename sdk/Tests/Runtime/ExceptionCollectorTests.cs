using System;
using System.Collections.Generic;
using System.IO;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using ApmTestGame;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-4: exceptions embedded in error logs (OZX's GameEventBus), grouping,
    /// breadcrumbs, and the cost of the repeat path.
    /// </summary>
    [TestFixture]
    public class ExceptionCollectorTests
    {
        private ExceptionCollector _collector;
        private List<ExceptionEvent> _events;

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            _events = new List<ExceptionEvent>();
            _collector = new ExceptionCollector(new ApmConfig(), null);
            _collector.EnqueueForTests = e => _events.Add(e);
            _collector.ActivateForTests();
        }

        [TearDown]
        public void TearDown()
        {
            GameContext.ResetForTests();
        }

        private Handlers.MiniBus Bus(params Action<int>[] handlers)
        {
            var bus = new Handlers.MiniBus((msg, stack) => _collector.HandleLogMessage(msg, stack, LogType.Error));
            foreach (var h in handlers)
                bus.Subscribe(h);
            return bus;
        }

        private static ExceptionParser.Result Parse(ExceptionEvent e) =>
            ExceptionParser.Analyze(e.log_type, e.message, e.stack, e.embedded_exception);

        // APM-1 verification 1: two handlers throwing different exceptions are
        // two groups, each with its real type and throw location.
        [Test]
        public void BusHandlers_ThrowingDifferentExceptions_AreTwoGroups()
        {
            Bus(Handlers.LoadRoomTemplate, Handlers.SpawnWave).Publish(12);

            Assert.AreEqual(2, _events.Count);
            Assert.AreNotEqual(_events[0].fingerprint, _events[1].fingerprint);
            var loader = Parse(_events[0]);
            var spawner = Parse(_events[1]);
            Assert.AreEqual("System.InvalidOperationException", loader.Type);
            Assert.AreEqual("System.NullReferenceException", spawner.Type);
            StringAssert.Contains("ApmTestGame.Handlers.LoadRoomTemplate (at ", loader.ThrowSite);
            StringAssert.Contains("ExceptionCollectorTests.cs:", loader.ThrowSite, "Mono in the Editor has file and line");
            StringAssert.Contains("ApmTestGame.Handlers.SpawnWave", spawner.ThrowSite);
            StringAssert.Contains("ApmTestGame.Handlers+MiniBus.Publish", loader.CallSite);
            Assert.AreEqual(_events[0].fingerprint, loader.Fingerprint, "the server computes the same fingerprint from what is sent");
            foreach (var e in _events)
            {
                Assert.AreEqual("error", e.log_type);
                StringAssert.StartsWith("System.", e.embedded_exception);
                Assert.AreEqual(1, e.count);
            }
        }

        // APM-1 verification 2: the same exception with different roomIds is one group.
        [Test]
        public void SameException_DifferentRoomIds_IsOneGroupWithACount()
        {
            var bus = Bus(Handlers.LoadRoomTemplate);
            bus.Publish(12);
            bus.Publish(7);
            bus.Publish(1043);

            Assert.AreEqual(1, _events.Count, "only the first occurrence is sent at once");
            _collector.FlushPending();
            Assert.AreEqual(2, _events.Count);
            var repeat = _events[1];
            Assert.AreEqual(2, repeat.count);
            Assert.AreEqual(_events[0].fingerprint, repeat.fingerprint);
            // The repeat count carries the first occurrence's text, so the
            // server computes the same fingerprint for it.
            Assert.AreEqual(_events[0].message, repeat.message);
            Assert.AreEqual(_events[0].embedded_exception, repeat.embedded_exception);
            Assert.AreEqual(_events[0].stack, repeat.stack);
            Assert.AreEqual(_events[0].log_type, repeat.log_type);
            Assert.IsNull(repeat.breadcrumbs);
            Assert.AreEqual("Room template not found for roomId=<v> (room_<n>)", Parse(_events[0]).MessageTemplate);
        }

        [Test]
        public void InnerExceptionChain_IsKept()
        {
            Bus(Handlers.LoadSave).Publish(3);

            var r = Parse(_events[0]);
            Assert.AreEqual(2, r.Chain.Count);
            Assert.AreEqual("System.IO.InvalidDataException", r.Chain[0].Type);
            Assert.AreEqual("System.FormatException", r.Chain[1].Type);
            Assert.AreEqual("Unexpected character at 3", r.Chain[1].Message);
            Assert.That(r.Chain[0].Frames.Count, Is.GreaterThan(0));
            Assert.That(r.Chain[1].Frames.Count, Is.GreaterThan(0));
            Assert.That(r.Chain[1].Frames[0].Line, Is.GreaterThan(0));
            CollectionAssert.AreEqual(new[] { "System.FormatException: Unexpected character at 3" }, ExceptionParser.InnerExceptions(r));
        }

        // The embedded exception is parsed before the message is cut to 500
        // characters, and kept under the stack's limit.
        [Test]
        public void EmbeddedException_IsExtractedBeforeTheMessageIsCut()
        {
            string ex = "System.InvalidOperationException: " + new string('m', 450) + "\n";
            for (int i = 0; i < 40; i++)
                ex += "  at Ozx.Rooms.RoomLoader.Step" + i + " (System.Int32 roomId) [0x00000] in /Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:" + (10 + i) + " \n";
            _collector.HandleLogMessage("[GameEventBus] Error handling event RoomEnteredEvent: " + ex, "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n", LogType.Error);

            var e = _events[0];
            Assert.AreEqual(ExceptionCollector.MaxMessageLength, e.message.Length);
            Assert.That(e.embedded_exception.Length, Is.GreaterThan(4000).And.LessThanOrEqualTo(ExceptionCollector.MaxEmbeddedLength));
            StringAssert.Contains("Step39", e.embedded_exception);
            var r = Parse(e);
            Assert.AreEqual("Ozx.Rooms.RoomLoader.Step0 (at /Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:10)", r.ThrowSite);
        }

        [Test]
        public void LongEmbeddedException_IsCompactedToTheLimit_KeepingTheFingerprint()
        {
            string ex = "System.IO.InvalidDataException: bad ---> System.FormatException: worse\n";
            for (int i = 0; i < 150; i++)
                ex += "  at Ozx.Json.Reader.Parse" + i + " (System.String s) [0x00000] in /Users/builder/ozx/Assets/Scripts/Json/Reader.cs:" + (i + 1) + " \n";
            ex += "   --- End of inner exception stack trace ---\n";
            for (int i = 0; i < 150; i++)
                ex += "  at Ozx.Save.Loader.Load" + i + " (System.String s) [0x00000] in /Users/builder/ozx/Assets/Scripts/Save/Loader.cs:" + (i + 1) + " \n";
            _collector.HandleLogMessage("Load failed: " + ex, "", LogType.Error);

            var e = _events[0];
            Assert.That(e.embedded_exception.Length, Is.LessThanOrEqualTo(ExceptionCollector.MaxEmbeddedLength));
            StringAssert.Contains("frames omitted", e.embedded_exception);
            var compacted = Parse(e);
            var full = ExceptionParser.Analyze("error", "x", "", ex);
            Assert.AreEqual(full.Fingerprint, compacted.Fingerprint);
            Assert.AreEqual("Ozx.Save.Loader.Load0 (at /Users/builder/ozx/Assets/Scripts/Save/Loader.cs:1)", compacted.ThrowSite);
            Assert.AreEqual(2, compacted.Chain.Count);
        }

        [Test]
        public void LoggedException_HasLogTypeException_AndNoEmbeddedText()
        {
            // The "logexception editor" case of the server's testdata/vectors.json.
            _collector.HandleLogMessage("NullReferenceException: Object reference not set to an instance of an object",
                "Ozx.Enemies.EnemyAI.UpdateTarget () (at Assets/Scripts/Enemies/EnemyAI.cs:57)\nOzx.Enemies.EnemyAI.Update () (at Assets/Scripts/Enemies/EnemyAI.cs:31)\n",
                LogType.Exception);

            var e = _events[0];
            Assert.AreEqual("exception", e.log_type);
            Assert.IsTrue(string.IsNullOrEmpty(e.embedded_exception));
            Assert.AreEqual("820dbeaa9ed209cd", e.fingerprint, "same fingerprint as the server");
        }

        [Test]
        public void PlainError_IsNotWrapped()
        {
            _collector.HandleLogMessage("Asset not found: anim/arack_red", "UnityEngine.Debug:LogError (object)\nOzx.Pool.Factory:Load (string) (at Assets/Pool/Factory.cs:33)\n", LogType.Error);
            _collector.HandleLogMessage("Assertion failed", "UnityEngine.Debug:Assert (bool)\nOzx.Combat.Health:Apply (int)\n", LogType.Assert);

            Assert.AreEqual("error", _events[0].log_type);
            Assert.AreEqual("assert", _events[1].log_type);
            Assert.IsTrue(string.IsNullOrEmpty(_events[0].embedded_exception));
            Assert.AreEqual("Ozx.Pool.Factory.Load (at Assets/Pool/Factory.cs:33)", Parse(_events[0]).CallSite);
        }

        [Test]
        public void SdkOwnOutput_IsIgnored()
        {
            _collector.HandleLogMessage(ApmClient.LogPrefix + " Internal error in X: NullReferenceException", "", LogType.Error);
            _collector.HandleLogMessage("a warning", "", LogType.Warning);
            Assert.AreEqual(0, _events.Count);
        }

        // Breadcrumbs (APM-1: "breadcrumbs are not uploaded with exceptions").
        [Test]
        public void FirstOccurrence_CarriesTheMostRecentBreadcrumbs()
        {
            for (int i = 0; i < 60; i++)
                _collector.RecordBreadcrumb("room r-" + i + " " + new string('x', i == 59 ? 500 : 0));
            Bus(Handlers.LoadRoomTemplate).Publish(12);

            var crumbs = _events[0].breadcrumbs;
            Assert.AreEqual(ExceptionCollector.MaxBreadcrumbsPerEvent, crumbs.Count);
            StringAssert.StartsWith("[", crumbs[0]);
            StringAssert.Contains("] room r-40 ", crumbs[0], "oldest of the last 20 first");
            StringAssert.Contains("] room r-59 ", crumbs[19]);
            Assert.AreEqual(ExceptionCollector.MaxBreadcrumbLength, crumbs[19].Length);
            Assert.AreEqual(':', crumbs[0][3]);
            Assert.AreEqual('.', crumbs[0][9]);

            // The exception itself becomes a breadcrumb of the next one.
            Bus(Handlers.SpawnWave).Publish(1);
            StringAssert.Contains("Exception: [GameEventBus] Error handling event RoomEnteredEvent: System.InvalidOperationException", _events[1].breadcrumbs[19]);
            Assert.AreEqual(50, _collector.GetBreadcrumbs().Count);
        }

        [Test]
        public void RecordBreadcrumb_DoesNotAllocate()
        {
            string crumb = "room r-12";
            _collector.RecordBreadcrumb(crumb);
            Assert.That(() => _collector.RecordBreadcrumb(crumb), Is.Not.AllocatingGCMemory());
        }

        // APM-3's budget: the same error every frame costs no allocation after
        // its first occurrence (before APM-4: Regex + MD5 + formatting, about
        // 2.4 KB per call).
        [Test]
        public void RepeatPath_DoesNotAllocate()
        {
            Bus(Handlers.SpawnWave).Publish(1); // warm up the bus itself
            string message = "[GameEventBus] Error handling event RoomEnteredEvent: " + SpawnWaveText;
            string stack = "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\nOzx.Rooms.RoomManager:EnterRoom(Int32)\n";
            _collector.HandleLogMessage(message, stack, LogType.Error);
            _collector.HandleLogMessage(message, stack, LogType.Error);
            int before = _events.Count;

            Assert.That(() => _collector.HandleLogMessage(message, stack, LogType.Error), Is.Not.AllocatingGCMemory());
            Assert.AreEqual(before, _events.Count, "repeats are only counted");
            _collector.FlushPending();
            Assert.AreEqual(before + 1, _events.Count);
            Assert.AreEqual(2, _events[before].count);
        }

        // An error logged every frame with a changing number (roomId, entity
        // id) is a repeat too: numbers in the message's first line do not
        // make a new text.
        [Test]
        public void RepeatPath_WithAChangingNumber_DoesNotAllocate()
        {
            string stack = "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n";
            var messages = new string[4];
            for (int i = 0; i < messages.Length; i++)
                messages[i] = "[GameEventBus] Error handling event RoomEnteredEvent: " + RoomLoaderText(100 + i * 7919);
            _collector.HandleLogMessage(messages[0], stack, LogType.Error);
            _collector.HandleLogMessage(messages[1], stack, LogType.Error);
            string third = messages[2];

            Assert.That(() => _collector.HandleLogMessage(third, stack, LogType.Error), Is.Not.AllocatingGCMemory());
            _collector.HandleLogMessage(messages[3], stack, LogType.Error);
            Assert.AreEqual(1, _events.Count);
            _collector.FlushPending();
            Assert.AreEqual(3, _events[1].count);
        }

        // Numbers elsewhere (here: in the frames) still separate texts, and so groups.
        [Test]
        public void NumbersInFrames_StillSeparateGroups()
        {
            string stack = "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n";
            _collector.HandleLogMessage("Failed: " + RoomLoaderText(5).Replace("LoadTemplate", "LoadStage1"), stack, LogType.Error);
            _collector.HandleLogMessage("Failed: " + RoomLoaderText(5).Replace("LoadTemplate", "LoadStage2"), stack, LogType.Error);
            Assert.AreEqual(2, _events.Count);
            Assert.AreNotEqual(_events[0].fingerprint, _events[1].fingerprint);
        }

        private static string RoomLoaderText(int roomId) =>
            "System.InvalidOperationException: Room template not found for roomId=" + roomId + "\n" +
            "  at Ozx.Rooms.RoomLoader.LoadTemplate (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n" +
            "  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in <00000000000000000000000000000000>:0 \n";

        private const string SpawnWaveText = "System.NullReferenceException: Object reference not set to an instance of an object.\n" +
            "  at Ozx.Rooms.EnemySpawner.SpawnWave (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n" +
            "  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in <00000000000000000000000000000000>:0 \n";
    }
}

namespace ApmTestGame
{
    // Stand-ins for game code, outside the OzxApm namespace (whose frames are
    // not in-app). A miniature of OZX's GameEventBus and three handlers.
    internal static class Handlers
    {
        // A miniature of OZX's GameEventBus: handler exceptions are caught and
        // logged as "[GameEventBus] Error handling event {EventType}: {ex}".
        internal sealed class MiniBus
        {
            private readonly List<Action<int>> _handlers = new List<Action<int>>();
            private readonly Action<string, string> _logError;

            public MiniBus(Action<string, string> logError)
            {
                _logError = logError;
            }

            public void Subscribe(Action<int> handler) => _handlers.Add(handler);

            public void Publish(int roomId)
            {
                foreach (var h in _handlers)
                {
                    try
                    {
                        h(roomId);
                    }
                    catch (Exception ex)
                    {
                        // Unity's stack of the LogError call, as the log callback gets it.
                        _logError($"[GameEventBus] Error handling event RoomEnteredEvent: {ex}", StackTraceUtility.ExtractStackTrace());
                    }
                }
            }
        }

        internal static void LoadRoomTemplate(int roomId)
        {
            throw new InvalidOperationException($"Room template not found for roomId={roomId} (room_{roomId})");
        }

        internal static void SpawnWave(int roomId)
        {
            string pool = roomId > 0 ? null : "x";
            Debug.Log(pool.Length);
        }

        internal static void LoadSaveVia(int roomId)
        {
            LoadSave(roomId);
        }

        internal static void LoadSave(int roomId)
        {
            try
            {
                throw new FormatException($"Unexpected character at {roomId}");
            }
            catch (Exception ex)
            {
                throw new InvalidDataException("Save file 'inventory.json' is corrupt", ex);
            }
        }
    }
}
