using System;
using NUnit.Framework;
using UnityEngine;
using OzxApm.Collectors;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-4: the SDK's port of the server's exception parser and fingerprint.
    /// The full set of cases is server/internal/fingerprint/testdata/vectors.json
    /// (checked against this class outside Unity, see RELEASE.md); the
    /// fingerprints below are copied from it.
    /// </summary>
    [TestFixture]
    public class ExceptionParserTests
    {
        private const string Il2CppNoSymbols = "[0x00000] in <00000000000000000000000000000000>:0 ";

        private static string RoomLoaderException(bool il2cpp)
        {
            string loc(string file, int line) => il2cpp ? Il2CppNoSymbols : "[0x0001b] in /Users/dev/ozx/Assets/Scripts/" + file + ":" + line + " ";
            return "System.InvalidOperationException: Room template not found for roomId=7 (room_7)\n" +
                   "  at Ozx.Rooms.RoomLoader.LoadTemplate (System.Int32 roomId) " + loc("Rooms/RoomLoader.cs", 71) + "\n" +
                   "  at Ozx.Rooms.RoomLoader.OnRoomEntered (Ozx.Core.RoomEnteredEvent e) " + loc("Rooms/RoomLoader.cs", 64) + "\n" +
                   "  at Ozx.Core.GameEventBus.Publish[T] (T evt) " + loc("Core/GameEventBus.cs", 48);
        }

        [Test]
        public void Fingerprints_MatchTheServer()
        {
            string ex = RoomLoaderException(true);
            Assert.AreEqual("fad75585e1523085", ExceptionParser.Analyze("error", "[GameEventBus] Error handling event RoomEnteredEvent: " + ex,
                "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\nOzx.Rooms.RoomManager:EnterRoom(Int32)\n", ex).Fingerprint, "bus ioe room 7 il2cpp");
            Assert.AreEqual("2fb4a13650efdcd4", ExceptionParser.Analyze("exception", "TimeoutException: Request 42 timed out after 3000 ms", "", "").Fingerprint);
            Assert.AreEqual("77167f58b194a9f0", ExceptionParser.Analyze("error", "Save write failed for slot 1 (88 bytes, id=0a1b2c3d-0000-4000-8000-000000000000)",
                "Ozx.Save.SaveSystem:Write(String)\n", "").Fingerprint);
            Assert.AreEqual("180d20f6d87bc4f9", ExceptionParser.Analyze("error", "\u623f\u95f4 12 \u672a\u627e\u5230 '\u68ee\u6797_3' \ud83d\ude00 ok=\u662f",
                "Ozx.UI.Panel:Open(String)\n", "").Fingerprint, "non-ASCII and a surrogate pair");
            Assert.AreEqual("0bd8609f8e2dd569", ExceptionParser.Analyze("", "Asset not found: anim/arack_red",
                "UnityEngine.Debug:LogError (object)\nOzx.Pool.Factory:Load (string) (at Assets/Pool/Factory.cs:33)\n", "").Fingerprint, "SDK before APM-4");
        }

        [Test]
        public void MonoAndIl2CppStacksOfTheSameCode_HaveTheSameFingerprint()
        {
            string mono = RoomLoaderException(false), il2cpp = RoomLoaderException(true);
            var a = ExceptionParser.Analyze("error", "x", "UnityEngine.Debug:LogError (object)\n", mono);
            var b = ExceptionParser.Analyze("error", "y", "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n", il2cpp);
            Assert.AreEqual(a.Fingerprint, b.Fingerprint);
            Assert.AreEqual("Ozx.Rooms.RoomLoader.LoadTemplate (at /Users/dev/ozx/Assets/Scripts/Rooms/RoomLoader.cs:71)", a.ThrowSite);
            Assert.AreEqual("Ozx.Rooms.RoomLoader.LoadTemplate", b.ThrowSite);
            var moved = ExceptionParser.Analyze("error", "x", "", mono.Replace(":71", ":171").Replace(":64", ":2"));
            Assert.AreEqual(a.Fingerprint, moved.Fingerprint, "line numbers do not count");
        }

        // The same real exception, as Unity reports it to log callbacks when it
        // is logged (Debug.LogException: innermost "Type: message", then
        // "Rethrow as"), and embedded in an error log ({ex}): one group.
        [Test]
        public void LoggedAndEmbeddedFormsOfARealException_HaveTheSameFingerprint()
        {
            Exception caught = null;
            try
            {
                ApmTestGame.Handlers.LoadSaveVia(7);
            }
            catch (Exception ex)
            {
                caught = ex;
            }
            string unity = StackTraceUtility.ExtractStringFromException(caught);
            int nl = unity.IndexOf('\n');
            var logged = ExceptionParser.Analyze("exception", unity.Substring(0, nl), unity.Substring(nl + 1), "");
            var embedded = ExceptionParser.Analyze("error", "x", "", caught.ToString());

            Assert.AreEqual(2, logged.Chain.Count);
            Assert.AreEqual("InvalidDataException", logged.Type);
            Assert.AreEqual("System.IO.InvalidDataException", embedded.Type);
            Assert.AreEqual("System.FormatException", embedded.Chain[1].Type);
            // The fingerprint uses the outer exception's first two in-app
            // frames, which both forms have (Unity appends the frames of the
            // logging call after them).
            Assert.AreEqual(embedded.Fingerprint, logged.Fingerprint, unity + "\n---\n" + caught);
            Assert.AreEqual("ApmTestGame.Handlers.LoadSave", embedded.Chain[0].Frames[0].Method);
            Assert.AreEqual("ApmTestGame.Handlers.LoadSave", logged.Chain[0].Frames[0].Method);
        }

        [Test]
        public void Template_ReplacesVariableParts()
        {
            Assert.AreEqual("Room template not found for roomId=<v> (room_<n>)", ExceptionParser.Template("Room template not found for roomId=12 (room_12)"));
            Assert.AreEqual("Room '*' id <guid> at <hex>, hash <hex>, took <n>s",
                ExceptionParser.Template("Room 'forest_3' id 3f2504e0-4f89-11d3-9a0c-0305e82c3301 at 0x7ffd, hash a3f9c2e1b7d4, took 1.25s"));
            Assert.AreEqual("first line", ExceptionParser.Template("first line\nsecond 123"));
            Assert.AreEqual(200, ExceptionParser.Template(new string('x', 1000)).Length);
        }

        [Test]
        public void NormalizeMethod_UnifiesMonoIl2CppAndUnityFormats()
        {
            Assert.AreEqual("Ozx.Core.GameEventBus.Publish", ExceptionParser.NormalizeMethod("Ozx.Core.GameEventBus:Publish<Ozx.Core.RoomEnteredEvent>"));
            Assert.AreEqual("Ozx.Core.GameEventBus.Publish", ExceptionParser.NormalizeMethod("Ozx.Core.GameEventBus.Publish[T]"));
            Assert.AreEqual("Ozx.Rooms.EnemySpawner+<>c.<Register>b", ExceptionParser.NormalizeMethod("Ozx.Rooms.EnemySpawner/<>c:<Register>b__2_0"));
            Assert.AreEqual("System.Collections.Generic.Dictionary.get_Item", ExceptionParser.NormalizeMethod("System.Collections.Generic.Dictionary`2[TKey,TValue].get_Item"));
        }

        [Test]
        public void ParseFrame_ReadsEveryFormat()
        {
            var mono = ExceptionParser.ParseFrame("  at Ozx.A.B (System.Int32 x) [0x0001a] in /p/A.cs:42 ");
            var il2cpp = ExceptionParser.ParseFrame("  at Ozx.A.B (System.Int32 x) " + Il2CppNoSymbols);
            var unity = ExceptionParser.ParseFrame("Ozx.A:B (int) (at Assets/A.cs:42)");
            var unityIl2Cpp = ExceptionParser.ParseFrame("Ozx.A:B(Int32)");
            var dotnet = ExceptionParser.ParseFrame("   at Ozx.A.B(Int32 x) in C:\\p\\A.cs:line 42");
            foreach (var f in new[] { mono, il2cpp, unity, unityIl2Cpp, dotnet })
                Assert.AreEqual("Ozx.A.B", f.Method);
            Assert.AreEqual(42, mono.Line);
            Assert.AreEqual("/p/A.cs", mono.File);
            Assert.AreEqual("", il2cpp.File);
            Assert.AreEqual("Assets/A.cs", unity.File);
            Assert.AreEqual("C:\\p\\A.cs", dotnet.File);
            Assert.IsNull(ExceptionParser.ParseFrame("System.NullReferenceException: Object reference not set (x)"));
            Assert.IsNull(ExceptionParser.ParseFrame("Room template not found for roomId=12 (room_12)"));
        }

        [Test]
        public void FindEmbedded_NeedsAStackOrAnInnerException()
        {
            Assert.AreEqual(9, ExceptionParser.FindEmbedded("Failed: (" + RoomLoaderException(true) + ")"));
            Assert.AreEqual(-1, ExceptionParser.FindEmbedded("[GameEventBus] Error handling event X: System.Exception: no stack"));
            Assert.AreEqual(-1, ExceptionParser.FindEmbedded("ExceptionHandler:Run failed\n  at A.B () [0x0] in <x>:0"));
            Assert.AreEqual(0, ExceptionParser.FindEmbedded("MyException: a ---> System.IO.IOException: b"));
            Assert.AreEqual(-1, ExceptionParser.FindEmbedded(null));
        }

        [Test]
        public void HostileInput_DoesNotThrow()
        {
            string huge = new string('(', 70000) + "Exception:\n  at " + new string('<', 5000);
            Assert.DoesNotThrow(() => ExceptionParser.Analyze("error", huge, huge, huge));
            Assert.DoesNotThrow(() => ExceptionParser.Analyze("exception", "\0", "Rethrow as X: y\nRethrow as\n(", ""));
            Assert.DoesNotThrow(() => ExceptionParser.FindEmbedded(huge));
            Assert.DoesNotThrow(() => ExceptionParser.Compact(huge, 8000));
            Assert.That(ExceptionParser.Compact(huge, 8000).Length, Is.LessThanOrEqualTo(8000));
        }
    }
}
