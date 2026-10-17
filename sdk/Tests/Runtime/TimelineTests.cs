using System;
using System.Collections.Generic;
using System.Linq;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-6: the breadcrumb time line (custom events, scenes, exceptions) and
    /// the recent warning/error lines, merged by time.
    /// </summary>
    [TestFixture]
    public class TimelineTests
    {
        private Timeline _t;

        [SetUp]
        public void SetUp()
        {
            _t = new Timeline();
        }

        private List<string> All() => _t.Snapshot(Timeline.MaxBreadcrumbs, Timeline.MaxLogLines, 1000, 1000);

        private static string Body(string line) => line.Substring(line.IndexOf("] ", StringComparison.Ordinal) + 2);

        [Test]
        public void OnlyWarningsErrorsAndAsserts_AreRecorded()
        {
            Assert.IsFalse(_t.OnLog("plain log", "", LogType.Log, true));
            Assert.IsFalse(_t.OnLog("an exception", "", LogType.Exception, true));
            Assert.IsFalse(_t.OnLog(ApmClient.LogPrefix + " own warning", "", LogType.Warning, true));
            Assert.IsFalse(_t.OnLog("", "", LogType.Warning, true));
            Assert.IsFalse(_t.OnLog(null, "", LogType.Warning, true));
            Assert.IsTrue(_t.OnLog("Asset not found: anim/arack_red", "", LogType.Warning, true));
            Assert.IsTrue(_t.OnLog("Data not found", "", LogType.Error, false));
            Assert.IsTrue(_t.OnLog("assert failed", "", LogType.Assert, true));
            CollectionAssert.AreEqual(new[] { "Warning: Asset not found: anim/arack_red", "Error: Data not found", "Assert: assert failed" },
                All().Select(Body).ToArray());
            Assert.AreEqual(3, _t.LogLinesSeen);
        }

        [Test]
        public void EventsAndLogs_AreMergedInTimeOrder()
        {
            long now = Timeline.NowMs();
            _t.RecordText("Scene: Main", now - 5000);
            _t.RecordEvent("room_enter", new[] { "roomId", "12" }, now - 3000);
            _t.OnLog("Asset not found: anim/arack_red", "", LogType.Warning, true); // now
            _t.RecordEvent("wave_start", null, Timeline.NowMs());

            var lines = All();
            CollectionAssert.AreEqual(new[]
            {
                "Scene: Main",
                "Event: room_enter roomId=12",
                "Warning: Asset not found: anim/arack_red",
                "Event: wave_start"
            }, lines.Select(Body).ToArray());
            StringAssert.IsMatch(@"^\[\d\d:\d\d:\d\d\.\d\d\d\] ", lines[0]);
        }

        // A warning logged every frame (with a changing number) is one line
        // with a count; it does not push the earlier lines out.
        [Test]
        public void RepeatedLinesOfTheSameShape_AreCountedNotStored()
        {
            _t.OnLog("Asset not found: anim/arack_red", "", LogType.Warning, true);
            for (int i = 0; i < 500; i++)
                _t.OnLog("Pool not found for enemy " + i, "", LogType.Warning, true);
            var lines = All();
            Assert.AreEqual(2, lines.Count);
            StringAssert.Contains("Warning: Asset not found: anim/arack_red", lines[0]);
            StringAssert.Contains("Warning: Pool not found for enemy 0 (x500 until ", lines[1]);
            Assert.AreEqual(501, _t.LogLinesSeen);
        }

        [Test]
        public void SameTextAsWarningAndError_AreTwoLines()
        {
            _t.OnLog("Data not found", "", LogType.Warning, true);
            _t.OnLog("Data not found", "", LogType.Error, true);
            Assert.AreEqual(2, All().Count);
        }

        [Test]
        public void LogSpam_DoesNotEvictCustomEventsOrErrors()
        {
            _t.RecordEvent("room_enter", null, Timeline.NowMs());
            _t.OnLog("[JsonDataRepository] Error loading enemies.json", "", LogType.Error, true);
            for (int i = 0; i < 200; i++)
                _t.OnLog("warning kind " + (char)('a' + i % 26) + (char)('a' + i / 26), "", LogType.Warning, true);
            var lines = All();
            Assert.AreEqual(2 + Timeline.MaxWarningLines, lines.Count);
            Assert.AreEqual("Event: room_enter", Body(lines[0]));
            Assert.AreEqual("Error: [JsonDataRepository] Error loading enemies.json", Body(lines[1]));
            // An exception takes the 10 most recent log lines: the error is not
            // among them, but it stays on the time line for later events.
            Assert.AreEqual(1 + 10, _t.Snapshot(20, 10, 200, 300).Count);
        }

        [Test]
        public void ErrorSpam_DoesNotEvictWarnings()
        {
            _t.OnLog("Asset not found: anim/arack_red", "", LogType.Warning, true);
            for (int i = 0; i < 100; i++)
                _t.OnLog("error kind " + (char)('a' + i % 26) + (char)('a' + i / 26), "", LogType.Error, true);
            var lines = All();
            Assert.AreEqual(1 + Timeline.MaxErrorLines, lines.Count);
            Assert.AreEqual("Warning: Asset not found: anim/arack_red", Body(lines[0]));
        }

        [Test]
        public void UrlQueryStrings_AreRemovedWhenSent()
        {
            _t.OnLog("Request failed: https://api.ozx.example/v1/save?token=SECRET&uid=42 (500)", "", LogType.Warning, true);
            _t.RecordText("Loaded http://cdn.ozx.example/a.bundle#frag and HTTPS://x.example/p?k=v");
            var lines = All().Select(Body).ToList();
            CollectionAssert.AreEqual(new[]
            {
                "Warning: Request failed: https://api.ozx.example/v1/save?... (500)",
                "Loaded http://cdn.ozx.example/a.bundle?... and HTTPS://x.example/p?..."
            }, lines);
        }

        [Test]
        public void Snapshot_TakesTheMostRecentOfEachRing_AndCutsLines()
        {
            long now = Timeline.NowMs();
            for (int i = 0; i < 30; i++)
                _t.RecordText("crumb " + i + new string('x', 300), now - 100 + i);
            _t.OnLog(new string('w', 1000), "", LogType.Warning, true);
            for (int i = 0; i < 5; i++)
                _t.OnLog("distinct " + (char)('a' + i), "", LogType.Error, true);

            var lines = _t.Snapshot(20, 3, 200, 300);
            Assert.AreEqual(23, lines.Count);
            StringAssert.StartsWith("crumb 10", Body(lines[0]));
            Assert.AreEqual(200, lines[0].Length);
            Assert.AreEqual("Error: distinct e", Body(lines[22]));
            Assert.IsTrue(lines.All(l => l.Length <= 300));
        }

        [Test]
        public void CountedLine_KeepsItsSuffixWhenCut()
        {
            string longLine = new string('w', 1000);
            _t.OnLog(longLine, "", LogType.Warning, true);
            _t.OnLog(longLine, "", LogType.Warning, true);
            var line = _t.Snapshot(0, 1, 200, 300)[0];
            Assert.LessOrEqual(line.Length, 300);
            StringAssert.Contains(" (x2 until ", line);
            StringAssert.EndsWith(")", line);
        }

        [Test]
        public void Snapshot_Since_LeavesOutOlderEntries()
        {
            long now = Timeline.NowMs();
            _t.RecordText("old", now - 60000);
            _t.RecordText("recent", now - 1000);
            _t.OnLog("repeating 1", "", LogType.Warning, true);
            var lines = _t.Snapshot(10, 10, 200, 300, now - 5000);
            CollectionAssert.AreEqual(new[] { "recent", "Warning: repeating 1" }, lines.Select(Body).ToArray());
        }

        [Test]
        public void Snapshot_ExcludesTheErrorBeingReported()
        {
            string current = "NullReferenceException: Object reference not set";
            _t.OnLog("Asset not found: anim/arack_red", "", LogType.Warning, true);
            _t.OnLog(current, "", LogType.Error, true);
            var lines = _t.Snapshot(10, 10, 200, 300, long.MinValue, current);
            CollectionAssert.AreEqual(new[] { "Warning: Asset not found: anim/arack_red" }, lines.Select(Body).ToArray());
        }

        [Test]
        public void HugeLine_IsStoredCut()
        {
            _t.OnLog(new string('w', 100000), "", LogType.Warning, true);
            var line = _t.Snapshot(0, 1, 0, int.MaxValue)[0];
            Assert.AreEqual(Timeline.MaxStoredLogLength, Body(line).Length - "Warning: ".Length);
        }

        [Test]
        public void Clear_ForgetsEverything()
        {
            _t.RecordText("a");
            _t.OnLog("b", "", LogType.Warning, true);
            _t.Clear();
            Assert.AreEqual(0, All().Count);
        }

        [Test]
        public void ShapeHash_IgnoresDigitValues()
        {
            Assert.AreEqual(Timeline.ShapeHash("roomId=12 failed"), Timeline.ShapeHash("roomId=1234 failed"));
            Assert.AreNotEqual(Timeline.ShapeHash("anim/arack_red"), Timeline.ShapeHash("anim/arack_blue"));
        }

        // The log callback's share: storing a line allocates nothing (strings
        // as captured locals: a literal inside the lambda would be counted).
        [Test]
        public void OnLog_DoesNotAllocate()
        {
            string warning = "Asset not found: anim/arack_red";
            string other = "Pool not found";
            string stack = "";
            _t.OnLog(warning, stack, LogType.Warning, true);
            Assert.That(() => { _t.OnLog(warning, stack, LogType.Warning, true); }, Is.Not.AllocatingGCMemory());
            Assert.That(() => { _t.OnLog(other, stack, LogType.Error, false); }, Is.Not.AllocatingGCMemory());
            Assert.That(() => { _t.OnLog(other, stack, LogType.Log, true); }, Is.Not.AllocatingGCMemory());
            // A long line logged again is only counted: no copy.
            string longLine = new string('w', Timeline.MaxStoredLogLength + 1000);
            _t.OnLog(longLine, stack, LogType.Warning, true);
            Assert.That(() => { _t.OnLog(longLine, stack, LogType.Warning, true); }, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void RecordingBreadcrumbsAndEvents_DoesNotAllocate()
        {
            string text = "Scene: Main";
            string name = "room_enter";
            string[] props = { "roomId", "12" };
            long t = Timeline.NowMs();
            _t.RecordText(text);
            _t.RecordEvent(name, props, t);
            Assert.That(() => _t.RecordText(text), Is.Not.AllocatingGCMemory());
            Assert.That(() => _t.RecordEvent(name, props, t), Is.Not.AllocatingGCMemory());
        }

        // Janks carry what happened in the 10 s before them.
        [Test]
        public void JankRecentEvents_AreTheLastTenSecondsBeforeTheJank()
        {
            long now = Timeline.NowMs();
            _t.RecordEvent("long_ago", null, now - 60000);
            _t.RecordEvent("room_enter", new[] { "roomId", "7" }, now - 4000);
            _t.OnLog("Asset not found: fx/boss", "", LogType.Warning, true);
            var recent = JankDetector.RecentEvents(_t, Timeline.NowMs(), 500);
            CollectionAssert.AreEqual(new[] { "Event: room_enter roomId=7", "Warning: Asset not found: fx/boss" }, recent.Select(Body).ToArray());
            Assert.AreEqual(0, JankDetector.RecentEvents(null, now, 100).Count);
        }
    }
}
