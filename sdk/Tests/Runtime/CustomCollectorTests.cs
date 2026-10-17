using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using NUnit.Framework;
using UnityEngine;
using UnityEngine.TestTools.Constraints;
using OzxApm.Collectors;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>APM-6: custom events, metrics, timers and the breadcrumbs exceptions carry.</summary>
    [TestFixture]
    public class CustomCollectorTests
    {
        private CustomCollector _collector;
        private Timeline _timeline;
        private List<BaseEvent> _events;
        private SessionClock _clock;

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            TimerRegistry.ResetForTests();
            _events = new List<BaseEvent>();
            _timeline = new Timeline();
            _clock = new SessionClock();
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 1000, "s-1");
            _collector = NewCollector(new ApmConfig());
        }

        [TearDown]
        public void TearDown()
        {
            GameContext.ResetForTests();
            TimerRegistry.ResetForTests();
        }

        private CustomCollector NewCollector(ApmConfig config)
        {
            var c = new CustomCollector(config, null, _timeline);
            c.EnqueueForTests = e =>
            {
                lock (_events)
                    _events.Add(e);
            };
            c.ClockForTests = _clock;
            c.ActivateForTests();
            return c;
        }

        private List<CustomEvent> Events() => _events.OfType<CustomEvent>().ToList();
        private List<CustomMetricEvent> Metrics() => _events.OfType<CustomMetricEvent>().ToList();

        private static string Body(string line) => line.Substring(line.IndexOf("] ", StringComparison.Ordinal) + 2);

        // ---- Events ----

        [Test]
        public void TrackEvent_IsSentWithPropsAndContext_AndGoesOnTheTimeline()
        {
            GameContext.Set("roomId", "12");
            Assert.IsTrue(_collector.TrackEvent("room_enter", CustomCollector.ValidateProps(new[] { "seed", "99", "mode", "hard" }, 4)));

            var e = Events().Single();
            Assert.AreEqual("room_enter", e.name);
            Assert.AreEqual(1, e.count);
            Assert.IsFalse(e.aggregated);
            CollectionAssert.AreEqual(new[] { "seed", "99", "mode", "hard" }, e.props);
            Assert.IsTrue(e.ctx.TryGetValue("roomId", out var room) && room == "12");
            Assert.Greater(e.timestamp, 0);
            Assert.AreEqual("Event: room_enter seed=99 mode=hard", Body(_timeline.Snapshot(10, 10, 200, 300).Single()));
        }

        [Test]
        public void TrackEvent_InvalidName_IsRefused()
        {
            Assert.IsFalse(_collector.TrackEvent("room enter", null));
            Assert.IsFalse(_collector.TrackEvent("", null));
            Assert.IsFalse(_collector.TrackEvent(null, null));
            Assert.IsFalse(_collector.TrackEvent(new string('a', 65), null));
            Assert.AreEqual(0, _events.Count);
            Assert.AreEqual(4, CustomCollector.RejectedCount);
        }

        [Test]
        public void Props_AreValidatedLikeTheGameContext()
        {
            string longValue = new string('v', 300);
            var props = CustomCollector.ValidateProps(new[]
            {
                "ok", "1",
                "bad key", "x",     // invalid key: left out
                "empty", "",        // empty value: left out
                "ok", "2",          // duplicate: the first wins
                "long", longValue,  // cut to 256
                null, "y"
            }, 12);
            CollectionAssert.AreEqual(new[] { "ok", "1", "long", longValue.Substring(0, 256) }, props);
            Assert.IsNull(CustomCollector.ValidateProps(new[] { "bad key", "x" }, 2));
            Assert.IsNull(CustomCollector.ValidateProps(null, 0));

            var many = new List<string>();
            for (int i = 0; i < 80; i++)
            {
                many.Add("k" + i);
                many.Add("v");
            }
            Assert.AreEqual(2 * CustomCollector.MaxProps, CustomCollector.ValidateProps(many.ToArray(), many.Count).Length);

            var big = new List<string>();
            for (int i = 0; i < 30; i++)
            {
                big.Add("key" + i);
                big.Add(new string('x', 250));
            }
            var kept = CustomCollector.ValidateProps(big.ToArray(), big.Count);
            int size = 0;
            for (int i = 0; i < kept.Length; i += 2)
                size += kept[i].Length + kept[i + 1].Length + 6;
            Assert.LessOrEqual(size, CustomCollector.MaxPropsLength);
            Assert.AreEqual(15, kept.Length / 2);
        }

        [Test]
        public void ValidProps_AreKeptWithoutACopy()
        {
            var pairs = new[] { "a", "1", "b", "2" };
            Assert.AreSame(pairs, CustomCollector.ValidateProps(pairs, 4));
        }

        // Beyond the per-minute budget calls are counted per name and context:
        // sum(count) stays exact, properties are not kept.
        [Test]
        public void BeyondTheBudget_EventsAreCountedPerNameAndContext()
        {
            var c = NewCollector(new ApmConfig { MaxCustomEventsPerMinute = 3 });
            GameContext.Set("roomId", "1");
            for (int i = 0; i < 5; i++)
                c.TrackEvent("enemy_killed", new[] { "type", "bat" });
            GameContext.Set("roomId", "2");
            for (int i = 0; i < 4; i++)
                c.TrackEvent("enemy_killed", null);
            c.TrackEvent("chest_open", null);

            Assert.AreEqual(3, Events().Count, "the budget, sent at once");
            c.FlushPending();
            var all = Events();
            Assert.AreEqual(10, all.Sum(e => e.count));
            var room1 = all.Single(e => e.aggregated && e.name == "enemy_killed" && e.ctx.TryGetValue("roomId", out var r) && r == "1");
            Assert.AreEqual(2, room1.count);
            Assert.IsNull(room1.props);
            Assert.AreEqual("s-1", room1.session_id);
            Assert.AreEqual(4, all.Single(e => e.aggregated && e.name == "enemy_killed" && e.ctx.TryGetValue("roomId", out var r) && r == "2").count);
            Assert.AreEqual(1, all.Single(e => e.name == "chest_open").count);

            // A new window has a new budget.
            c.TrackEvent("chest_open", null);
            Assert.IsFalse(Events().Last().aggregated);
            Assert.AreEqual(9, _timeline.Snapshot(50, 0, 200, 200).Count(l => l.Contains("Event: enemy_killed")));
        }

        // ---- Metrics ----

        [Test]
        public void Metrics_AreAggregatedPerNameAndContext()
        {
            GameContext.Set("roomId", "1");
            foreach (double v in new[] { 100.0, 300, 200 })
                _collector.RecordMetric("room_switch_ms", v, "ms");
            GameContext.Set("roomId", "2");
            _collector.RecordMetric("room_switch_ms", 900, "ms");
            _collector.RecordMetric("gold", 5, null);
            Assert.AreEqual(0, _events.Count, "nothing before the window ends");

            _collector.FlushPending();
            var metrics = Metrics();
            Assert.AreEqual(3, metrics.Count);
            var r1 = metrics.Single(m => m.name == "room_switch_ms" && m.ctx.TryGetValue("roomId", out var r) && r == "1");
            Assert.AreEqual(3, r1.count);
            Assert.AreEqual(600, r1.sum);
            Assert.AreEqual(100, r1.min);
            Assert.AreEqual(300, r1.max);
            Assert.AreEqual("ms", r1.unit);
            Assert.AreEqual(MetricBuckets.Version, r1.hist_v);
            Assert.AreEqual(3, r1.hist_keys.Length);
            Assert.AreEqual(3, r1.hist_counts.Sum());
            Assert.AreEqual("s-1", r1.session_id);
            Assert.GreaterOrEqual(r1.window_ms, 0);
            Assert.AreEqual("", metrics.Single(m => m.name == "gold").unit);

            // The window is over: the next sample starts a new series.
            _collector.RecordMetric("room_switch_ms", 1, "ms");
            _collector.FlushPending();
            Assert.AreEqual(4, Metrics().Count);
        }

        [Test]
        public void Metrics_InvalidSamples_AreRefused()
        {
            Assert.IsFalse(_collector.RecordMetric("x", double.NaN, null));
            Assert.IsFalse(_collector.RecordMetric("x", double.PositiveInfinity, null));
            Assert.IsFalse(_collector.RecordMetric("bad name", 1, null));
            Assert.IsTrue(_collector.RecordMetric("ok", 1, "bad\"unit"));
            _collector.FlushPending();
            Assert.AreEqual("", Metrics().Single().unit);
            Assert.AreEqual(3, CustomCollector.RejectedCount);
        }

        [Test]
        public void ManyContexts_GoToAMergedSeriesPerName()
        {
            for (int room = 0; room < CustomCollector.MaxSeriesPerName + 5; room++)
            {
                GameContext.Set("levelId", "3");
                GameContext.Set("roomId", room.ToString());
                _collector.RecordMetric("room_switch_ms", 10 + room, "ms");
            }
            _collector.FlushPending();
            var metrics = Metrics();
            Assert.AreEqual(CustomCollector.MaxSeriesPerName + 1, metrics.Count);
            var merged = metrics.Single(m => m.ctx_merged);
            Assert.AreEqual(5, merged.count);
            Assert.IsFalse(merged.ctx.TryGetValue("roomId", out _));
            Assert.IsTrue(merged.ctx.TryGetValue("levelId", out var level) && level == "3");
            Assert.AreEqual(CustomCollector.MaxSeriesPerName + 5, metrics.Sum(m => m.count));
        }

        [Test]
        public void ManyNames_AreDroppedAndCounted()
        {
            for (int i = 0; i < CustomCollector.MaxNamesPerWindow + 3; i++)
                _collector.RecordMetric("m" + i, 1, null);
            _collector.FlushPending();
            Assert.AreEqual(CustomCollector.MaxNamesPerWindow, Metrics().Count);
            _collector.GetStats(out _, out _, out _, out long samples, out long dropped);
            Assert.AreEqual(CustomCollector.MaxNamesPerWindow, samples);
            Assert.AreEqual(3, dropped);
        }

        [Test]
        public void ANewSession_ClosesTheWindow()
        {
            _collector.RecordMetric("load_ms", 5, "ms");
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(), "s-2");
            _collector.RecordMetric("load_ms", 7, "ms");
            Assert.AreEqual("s-1", Metrics().Single().session_id, "closed by the first sample of the new session");
            _collector.FlushPending();
            Assert.AreEqual("s-2", Metrics().Last().session_id);
        }

        [Test]
        public void Update_ClosesTheWindowAfterAMinute()
        {
            _collector.RecordMetric("load_ms", 5, "ms");
            _collector.Update();
            Assert.AreEqual(0, _events.Count);
            _collector.ExpireWindowForTests();
            _collector.Update();
            Assert.AreEqual(1, Metrics().Count);
            _collector.Update();
            Assert.AreEqual(1, _events.Count);
        }

        [Test]
        public void Stopped_RecordsNothing()
        {
            _collector.RecordMetric("load_ms", 5, "ms");
            _collector.Stop();
            Assert.AreEqual(1, Metrics().Count, "Stop flushes");
            Assert.IsFalse(_collector.RecordMetric("load_ms", 5, "ms"));
            Assert.IsFalse(_collector.TrackEvent("x", null));
            _collector.FlushPending();
            Assert.AreEqual(1, _events.Count);
        }

        [Test]
        public void ConcurrentSamples_AreAllCounted()
        {
            const int threads = 4, per = 20000;
            var workers = new Thread[threads];
            for (int t = 0; t < threads; t++)
            {
                int id = t;
                workers[t] = new Thread(() =>
                {
                    for (int i = 0; i < per; i++)
                        _collector.RecordMetric(id % 2 == 0 ? "a" : "b", i % 100, null);
                });
                workers[t].Start();
            }
            for (int i = 0; i < 50; i++)
                _collector.FlushPending();
            foreach (var w in workers)
                w.Join();
            _collector.FlushPending();
            Assert.AreEqual(threads * per, Metrics().Sum(m => m.count));
            Assert.AreEqual(threads * per, Metrics().Sum(m => m.hist_counts.Sum()));
        }

        // The hot path: a sample of a known name in a known context allocates
        // nothing, in this window or the next (series are reused).
        [Test]
        public void RecordMetric_DoesNotAllocate()
        {
            string name = "room_switch_ms";
            string unit = "ms";
            GameContext.Set("roomId", "1");
            _collector.RecordMetric(name, 1, unit);
            _collector.RecordMetric(name, 2000, unit);
            Assert.That(() => { _collector.RecordMetric(name, 1234.5, unit); }, Is.Not.AllocatingGCMemory());
            _collector.FlushPending();
            _collector.RecordMetric(name, 1, unit);
            Assert.That(() => { _collector.RecordMetric(name, 99, unit); }, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void IdleUpdate_DoesNotAllocate()
        {
            _collector.Update();
            Assert.That(() => _collector.Update(), Is.Not.AllocatingGCMemory());
            Assert.That(() => _collector.FlushPending(), Is.Not.AllocatingGCMemory());
        }

        // ---- Timers ----

        [Test]
        public void Timers_ByHandleAndByName()
        {
            var t = TimerRegistry.Start("room_switch_ms");
            Assert.IsTrue(t.IsValid);
            Assert.AreEqual("room_switch_ms", t.MetricName);
            Thread.Sleep(5);
            Assert.GreaterOrEqual(t.ElapsedMs, 4);
            Assert.IsTrue(TimerRegistry.TryTake("room_switch_ms", out var named));
            Assert.AreEqual(t.StartTicks, named.StartTicks);
            Assert.IsFalse(TimerRegistry.TryTake("room_switch_ms", out _), "taken once");

            var t2 = TimerRegistry.Start("load_ms");
            TimerRegistry.Forget(t2);
            Assert.IsFalse(TimerRegistry.TryTake("load_ms", out _), "stopping by handle forgets the name");

            var a = TimerRegistry.Start("x");
            var b = TimerRegistry.Start("x");
            TimerRegistry.Forget(a);
            Assert.IsTrue(TimerRegistry.TryTake("x", out var latest), "a later start of the name is kept");
            Assert.AreEqual(b.StartTicks, latest.StartTicks);
            Assert.IsFalse(default(ApmTimer).IsValid);
            Assert.AreEqual(0, default(ApmTimer).ElapsedMs);
            Assert.IsTrue(TimerRegistry.Cancel(TimerRegistry.Start("c").MetricName));
            Assert.IsFalse(TimerRegistry.Cancel("c"));
        }

        [Test]
        public void Timer_SpanningAPause_IsMarked()
        {
            var t = TimerRegistry.Start("room_switch_ms");
            TimerRegistry.OnPause();
            Assert.AreNotEqual(t.PauseEpoch, TimerRegistry.PauseEpoch);
        }

        [Test]
        public void StartAndStopTimer_DoNotAllocate()
        {
            string name = "room_switch_ms";
            TimerRegistry.Forget(TimerRegistry.Start(name));
            Assert.That(() => TimerRegistry.Forget(TimerRegistry.Start(name)), Is.Not.AllocatingGCMemory());
            Assert.That(() =>
            {
                TimerRegistry.Start(name);
                TimerRegistry.TryTake(name, out _);
            }, Is.Not.AllocatingGCMemory());
        }

        // ---- Wire format ----

        [Test]
        public void CustomEvent_Serialization()
        {
            GameContext.Set("roomId", "12");
            var e = new CustomEvent { name = "room_enter", props = new[] { "seed", "9\"9" }, ctx = GameContext.Current, timestamp = 1700000000000 };
            string json = JsonSerializer.SerializeEvent(e);
            StringAssert.Contains("\"type\":\"custom_event\"", json);
            StringAssert.Contains(",\"name\":\"room_enter\",\"count\":1,\"props\":{\"seed\":\"9\\\"9\"},\"ctx\":{\"roomId\":\"12\"}}", json);
            StringAssert.DoesNotContain("aggregated", json);

            var agg = new CustomEvent { name = "x", count = 7, aggregated = true, ctx_merged = true };
            StringAssert.Contains(",\"name\":\"x\",\"count\":7,\"aggregated\":true,\"ctx_merged\":true}", JsonSerializer.SerializeEvent(agg));
        }

        [Test]
        public void CustomMetric_Serialization()
        {
            var m = new CustomMetricEvent
            {
                name = "room_switch_ms", unit = "ms", count = 3, sum = 600.25, min = 100, max = 0.001, window_ms = 60000,
                hist_v = 1, hist_keys = new[] { 1117, 1146 }, hist_counts = new long[] { 2, 1 }, ctx_merged = true
            };
            string json = JsonSerializer.SerializeEvent(m);
            StringAssert.Contains(",\"name\":\"room_switch_ms\",\"unit\":\"ms\",\"count\":3,\"sum\":600.25,\"min\":100,\"max\":0.001,\"window_ms\":60000,\"hist_v\":1,\"hist_keys\":[1117,1146],\"hist_counts\":[2,1],\"ctx_merged\":true}", json);
            var none = new CustomMetricEvent { name = "x", sum = double.NaN };
            StringAssert.Contains("\"sum\":null", JsonSerializer.SerializeEvent(none));
            StringAssert.Contains("\"hist_keys\":[],\"hist_counts\":[]", JsonSerializer.SerializeEvent(none));
        }

        // ---- Breadcrumbs of exceptions (APM-1 verification 1) ----

        // OZX: "Asset not found" is logged as a warning, the room is entered,
        // and seconds later the pool's Instantiate throws an NRE that says
        // nothing. The NRE carries both, in time order.
        [Test]
        public void Exception_CarriesTheCustomEventsAndWarningsBeforeIt_InTimeOrder()
        {
            var exceptions = new List<ExceptionEvent>();
            var ec = new ExceptionCollector(new ApmConfig(), null, _timeline);
            ec.EnqueueForTests = exceptions.Add;
            ec.LogSink = _timeline;
            ec.ActivateForTests();

            ec.HandleLogMessage("Loading room 12", "", LogType.Log);
            _collector.TrackEvent("room_enter", new[] { "roomId", "12" });
            ec.HandleLogMessage("Asset not found: anim/arack_red", "", LogType.Warning);
            ec.HandleLogMessage("[JsonDataRepository] Error loading enemies.json", "", LogType.Error);
            _collector.TrackEvent("wave_start", null);
            ec.HandleLogMessage("NullReferenceException: Object reference not set to an instance of an object",
                "Ozx.Pool.PoolFactory.Instantiate ()\nOzx.Rooms.Room.Spawn ()\n", LogType.Exception);

            var nre = exceptions.Last();
            Assert.AreEqual("exception", nre.log_type);
            CollectionAssert.AreEqual(new[]
            {
                "Event: room_enter roomId=12",
                "Warning: Asset not found: anim/arack_red",
                "Error: [JsonDataRepository] Error loading enemies.json",
                "Event: wave_start"
            }, nre.breadcrumbs.Select(Body).ToArray());

            // The error was reported too; its own line is not in its breadcrumbs,
            // and it is not added again as an "Exception:" breadcrumb.
            var error = exceptions.First();
            CollectionAssert.AreEqual(new[] { "Event: room_enter roomId=12", "Warning: Asset not found: anim/arack_red" },
                error.breadcrumbs.Select(Body).ToArray());
            var after = _timeline.Snapshot(50, 32, 1000, 1000).Select(Body).ToList();
            Assert.AreEqual(1, after.Count(l => l.Contains("JsonDataRepository")));
            Assert.AreEqual(1, after.Count(l => l.StartsWith("Exception: NullReferenceException", StringComparison.Ordinal)));
        }
    }
}
