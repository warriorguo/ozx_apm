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
    /// <summary>
    /// APM-10: the game clock (t, frame) events carry, typed custom event
    /// properties (ApmClient.Event) and how both are serialized, for the
    /// session export in OZX's AiTelemetry format.
    /// </summary>
    [TestFixture]
    public class GameClockTests
    {
        private double _time;
        private int _frame;

        [SetUp]
        public void SetUp()
        {
            GameClock.ResetForTests();
            _time = 12.345678;
            _frame = 700;
            GameClock.TimeForTests = () => _time;
            GameClock.FrameForTests = () => _frame;
        }

        [TearDown]
        public void TearDown()
        {
            GameClock.ResetForTests();
        }

        [Test]
        public void BeforeInit_ThereIsNoClock()
        {
            Assert.IsFalse(GameClock.Now(out _, out int frame));
            Assert.AreEqual(-1, frame);
            var evt = new JankEvent();
            GameClock.Stamp(evt);
            Assert.AreEqual(-1, evt.frame);
        }

        // Nothing per frame: the main thread reads the clock when it records
        // an event and publishes it; other threads get the last published pair.
        [Test]
        public void MainThread_ReadsLive_OtherThreads_ReadTheLastMainThreadEvent()
        {
            GameClock.Init();
            _time = 20.5;
            _frame = 900;
            double otherT = -1;
            int otherFrame = -1;
            var thread = new Thread(() => GameClock.Now(out otherT, out otherFrame));
            thread.Start();
            thread.Join();
            Assert.AreEqual(12.345678, otherT, "published by Init");
            Assert.AreEqual(700, otherFrame);

            Assert.IsTrue(GameClock.Now(out double t, out int frame), "a main-thread event reads live");
            Assert.AreEqual(20.5, t);
            Assert.AreEqual(900, frame);
            thread = new Thread(() => GameClock.Now(out otherT, out otherFrame));
            thread.Start();
            thread.Join();
            Assert.AreEqual(20.5, otherT, "and publishes it");
            Assert.AreEqual(900, otherFrame);

            // Stamping an event on the main thread (a perf sample) publishes too.
            _time = 21.5;
            _frame = 960;
            GameClock.Stamp(new PerfSampleEvent());
            thread = new Thread(() => GameClock.Now(out otherT, out otherFrame));
            thread.Start();
            thread.Join();
            Assert.AreEqual(21.5, otherT);
            Assert.AreEqual(960, otherFrame);
        }

        // The published pair is never torn: a reader sees t and frame of the same update.
        [Test]
        public void PublishedPair_IsNeverTorn()
        {
            GameClock.Init();
            int bad = 0, reads = 0;
            bool stop = false;
            var reader = new Thread(() =>
            {
                while (!Volatile.Read(ref stop))
                {
                    if (GameClock.Now(out double rt, out int rf))
                    {
                        reads++;
                        if (rt != rf * 0.5)
                            bad++;
                    }
                }
            });
            reader.Start();
            for (int i = 1; i < 300000; i++)
            {
                _frame = i;
                _time = i * 0.5;
                GameClock.Now(out _, out _);
            }
            Volatile.Write(ref stop, true);
            reader.Join();
            Assert.AreEqual(0, bad, "torn reads of " + reads);
            Assert.Greater(reads, 0);
        }

        // APM-21 + APM-10: a jank carries the game clock of its first slow
        // frame (the frame before the one whose deltaTime reported it, like
        // its wall-clock time and context), not of the frame it is sent in.
        [Test]
        public void Jank_HasTheGameClockOfItsFirstSlowFrame()
        {
            GameClock.Init();
            double delta = 1.0 / 60;
            GameClock.DeltaForTests = () => delta;
            var d = new JankDetector(new ApmConfig(), null) { SentForTests = new List<JankEvent>(), TimelineForTests = new Timeline() };
            d.StartWith(60, 0);
            void Frame(float ms)
            {
                _frame++;
                delta = ms / 1000.0;
                _time += delta;
                d.UpdateWith(ms, 60, 0);
            }
            _frame = 100;
            _time = 10.0;
            for (int i = 0; i < 60; i++)
                Frame(16.7f);
            int framesBefore = _frame;
            double timeBefore = _time;
            Frame(30f); // the first slow frame: reported in frame framesBefore + 1
            Frame(30f); // now a jank
            for (int i = 0; i < 20; i++)
                Frame(16.7f);
            d.OnSceneLoaded(); // flush if still open
            Assert.AreEqual(1, d.SentForTests.Count);
            var e = d.SentForTests[0];
            Assert.AreEqual(framesBefore, e.frame, "the slow frame is the one before the frame that saw it");
            Assert.AreEqual(timeBefore, e.game_t, 1e-6, "its start");
            StringAssert.Contains("\"frame\":" + framesBefore, JsonSerializer.SerializeEvent(e));

            // No clock (outside the main thread's Init): none, stamped at enqueue as before.
            GameClock.ResetForTests();
            var d2 = new JankDetector(new ApmConfig(), null) { SentForTests = new List<JankEvent>(), TimelineForTests = new Timeline() };
            d2.StartWith(60, 0);
            for (int i = 0; i < 60; i++)
                d2.UpdateWith(16.7f, 60, 0);
            d2.UpdateWith(150f, 60, 0);
            d2.OnSceneLoaded();
            Assert.AreEqual(1, d2.SentForTests.Count);
            Assert.AreEqual(-1, d2.SentForTests[0].frame);
        }

        [Test]
        public void Stamp_KeepsAnExistingClock()
        {
            GameClock.Init();
            var evt = new SceneLoadEvent { game_t = 3, frame = 4 };
            GameClock.Stamp(evt);
            Assert.AreEqual(3, evt.game_t);
            Assert.AreEqual(4, evt.frame);
            var fresh = new SceneLoadEvent();
            GameClock.Stamp(fresh);
            Assert.AreEqual(12.345678, fresh.game_t);
            Assert.AreEqual(700, fresh.frame);
        }

        [Test]
        public void StampsClockAtEnqueue_OnlyForEventsEnqueuedWhenTheyHappen()
        {
            Assert.IsTrue(ApmClient.StampsClockAtEnqueue(new PerfSampleEvent()));
            Assert.IsTrue(ApmClient.StampsClockAtEnqueue(new JankEvent()));
            Assert.IsTrue(ApmClient.StampsClockAtEnqueue(new SceneLoadEvent()));
            Assert.IsTrue(ApmClient.StampsClockAtEnqueue(new StartupEvent()));
            Assert.IsTrue(ApmClient.StampsClockAtEnqueue(new RunEvent()));
            Assert.IsFalse(ApmClient.StampsClockAtEnqueue(new CustomEvent()), "stamped at the call");
            Assert.IsFalse(ApmClient.StampsClockAtEnqueue(new CustomMetricEvent()), "an aggregate over a window");
            Assert.IsFalse(ApmClient.StampsClockAtEnqueue(new ExceptionEvent()), "stamped at its first occurrence");
        }

        [Test]
        public void Serialized_WithTAndFrame_OnlyWhenStamped()
        {
            var evt = new JankEvent { timestamp = 1, game_t = 12.34567, frame = 704 };
            string json = JsonSerializer.SerializeEvent(evt);
            StringAssert.Contains("\"t\":12.3457,\"frame\":704", json);

            string none = JsonSerializer.SerializeEvent(new JankEvent { timestamp = 1 });
            StringAssert.DoesNotContain("\"t\":", none);
            StringAssert.DoesNotContain("\"frame\":", none);

            string zero = JsonSerializer.SerializeEvent(new JankEvent { timestamp = 1, game_t = 0, frame = 0 });
            StringAssert.Contains("\"t\":0.0000,\"frame\":0", zero);
        }

        [Test]
        public void Fixed_WritesFixedDecimals_LikeAiTelemetry()
        {
            Assert.AreEqual("0.0000", JsonLine.FixedString(0, 4));
            Assert.AreEqual("4.0000", JsonLine.FixedString(4, 4));
            Assert.AreEqual("12.4098", JsonLine.FixedString(12.40981, 4));
            Assert.AreEqual("34.322", JsonLine.FixedString(34.32199859619141, 3));
            Assert.AreEqual("-0.938", JsonLine.FixedString(-0.9375001, 3));
            Assert.AreEqual("0.000", JsonLine.FixedString(-0.0001, 3), "no sign on zero");
            Assert.AreEqual("2", JsonLine.FixedString(1.5, 0), "half away from zero");
            Assert.AreEqual("-2", JsonLine.FixedString(-1.5, 0));
            Assert.AreEqual("1.000000", JsonLine.FixedString(1, 9), "at most 6 decimals");
            Assert.AreEqual("1E+20", JsonLine.FixedString(1e20, 3), "round-trip form beyond 9e15 units");
            Assert.AreEqual("null", JsonLine.FixedString(double.NaN, 3));

            var w = new JsonLine();
            w.Fixed(49031.14, 3);
            w.Byte((byte)' ');
            w.Fixed(-7.25, 1);
            w.Byte((byte)' ');
            w.Fixed(double.PositiveInfinity, 2);
            Assert.AreEqual("49031.140 -7.3 null", w.ToUtf8String());
        }

        // Unity runtime (the constraint needs the profiler): reading and
        // publishing the clock and writing t/frame allocate nothing.
        [Test]
        public void ClockAndItsSerialization_DoNotAllocate()
        {
            GameClock.Init();
            var evt = new JankEvent { timestamp = 1, game_t = 1234.5678, frame = 99999 };
            var w = new JsonLine(8192);
            JsonSerializer.WriteEvent(w, evt);
            Assert.That(() =>
            {
                GameClock.Refresh();
                GameClock.Now(out _, out _);
                w.Clear();
                JsonSerializer.WriteEvent(w, evt);
            }, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void IsJsonLiteral_FollowsJsonsGrammar()
        {
            foreach (var ok in new[] { "0", "-0", "12", "-1.5", "0.25", "1e5", "1E+20", "2.5e-3" })
                Assert.IsTrue(JsonSerializer.IsJsonLiteral(PropKinds.Number, ok), ok);
            foreach (var bad in new[] { "", "-", "01", "1.", ".5", "1e", "NaN", "Infinity", "1,5", "0x10", " 1", "1 ", "+1", "\"1\"" })
                Assert.IsFalse(JsonSerializer.IsJsonLiteral(PropKinds.Number, bad), bad);
            Assert.IsTrue(JsonSerializer.IsJsonLiteral(PropKinds.Bool, "true"));
            Assert.IsTrue(JsonSerializer.IsJsonLiteral(PropKinds.Bool, "false"));
            Assert.IsFalse(JsonSerializer.IsJsonLiteral(PropKinds.Bool, "True"));
            Assert.IsFalse(JsonSerializer.IsJsonLiteral(PropKinds.String, "1"));
        }
    }

    [TestFixture]
    public class ApmEventTests
    {
        private CustomCollector _collector;
        private List<BaseEvent> _events;
        private SessionClock _clock;

        [SetUp]
        public void SetUp()
        {
            GameContext.ResetForTests();
            CustomCollector.ResetStaticsForTests();
            GameClock.ResetForTests();
            GameClock.TimeForTests = () => 37.25;
            GameClock.FrameForTests = () => 2171;
            GameClock.Init();
            _events = new List<BaseEvent>();
            _clock = new SessionClock();
            _clock.Begin(DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - 1000, "s-1");
            _collector = NewCollector(new ApmConfig());
        }

        [TearDown]
        public void TearDown()
        {
            GameContext.ResetForTests();
            GameClock.ResetForTests();
        }

        private CustomCollector NewCollector(ApmConfig config)
        {
            var c = new CustomCollector(config, null, new Timeline());
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

        [Test]
        public void TypedEvent_IsSentWithTypesAndTheGameClock()
        {
            Assert.IsTrue(EventBuilder.Begin(_collector, "playermovement.damaged")
                .Num("amount", 34.32199859619141).Num("rawAmount", 70).Str("srcKind", "EnemyProjectile")
                .Int("srcInstanceId", 2).Bool("isCrit", false).Vec("pos", 4.99f, 1.021f).Str("roomId", "f0_room_1_0")
                .Send());

            var e = Events().Single();
            Assert.AreEqual("playermovement.damaged", e.name);
            Assert.AreEqual(37.25, e.game_t);
            Assert.AreEqual(2171, e.frame);
            string json = JsonSerializer.SerializeEvent(e);
            StringAssert.Contains("\"t\":37.2500,\"frame\":2171", json);
            StringAssert.Contains("\"props\":{\"amount\":34.322,\"rawAmount\":70.000,\"srcKind\":\"EnemyProjectile\",\"srcInstanceId\":2," +
                                  "\"isCrit\":false,\"pos.x\":4.990,\"pos.y\":1.021,\"roomId\":\"f0_room_1_0\"}", json);
        }

        [Test]
        public void TrackEvent_CarriesTheGameClockToo()
        {
            Assert.IsTrue(_collector.TrackEvent("room_enter", CustomCollector.ValidateProps(new[] { "roomId", "12" }, 2)));
            var e = Events().Single();
            Assert.AreEqual(2171, e.frame);
            Assert.IsNull(e.prop_kinds);
            StringAssert.Contains("\"props\":{\"roomId\":\"12\"}", JsonSerializer.SerializeEvent(e));
        }

        [Test]
        public void AggregatedCounts_CarryNoClock()
        {
            var c = NewCollector(new ApmConfig { MaxCustomEventsPerMinute = 1 });
            _events.Clear();
            Assert.IsTrue(EventBuilder.Begin(c, "loot.drop").Str("item", "a").Send());
            Assert.IsTrue(EventBuilder.Begin(c, "loot.drop").Str("item", "b").Send());
            c.FlushPending();
            var events = Events();
            Assert.AreEqual(2, events.Count);
            Assert.IsFalse(events[0].aggregated);
            Assert.AreEqual(2171, events[0].frame);
            Assert.IsTrue(events[1].aggregated);
            Assert.AreEqual(-1, events[1].frame);
            StringAssert.DoesNotContain("\"t\":", JsonSerializer.SerializeEvent(events[1]));
        }

        [Test]
        public void PerNameBudget_IsConfigurable()
        {
            var c = NewCollector(new ApmConfig { MaxCustomEventsPerMinute = 200, MaxCustomEventsPerNamePerMinute = 30 });
            _events.Clear();
            for (int i = 0; i < 40; i++)
                EventBuilder.Begin(c, "playermovement.damaged").Num("amount", i).Send();
            c.FlushPending();
            var events = Events();
            Assert.AreEqual(30, events.Count(e => !e.aggregated));
            Assert.AreEqual(10, events.Where(e => e.aggregated).Sum(e => e.count));

            var dflt = NewCollector(new ApmConfig { MaxCustomEventsPerNamePerMinute = 0 });
            _events.Clear();
            for (int i = 0; i < 3; i++)
                EventBuilder.Begin(dflt, "x").Send();
            Assert.AreEqual(1, Events().Count(e => !e.aggregated), "below 1 reads as 1");
        }

        [Test]
        public void InvalidProperties_AreLeftOut_KindsStayAligned()
        {
            Assert.IsTrue(EventBuilder.Begin(_collector, "encounter.room_ready")
                .Str("roomId", "r1").Num("bad key", 1).Num("nan", double.NaN).Str("empty", "")
                .Bool("isCave", true).Str("folder", "room01_r1").Int("n", -3)
                .Send());
            var e = Events().Single();
            Assert.AreEqual(new[] { "roomId", "r1", "isCave", "true", "folder", "room01_r1", "n", "-3" }, e.props);
            Assert.AreEqual(new[] { PropKinds.String, PropKinds.Bool, PropKinds.String, PropKinds.Number }, e.prop_kinds);
            StringAssert.Contains("\"props\":{\"roomId\":\"r1\",\"isCave\":true,\"folder\":\"room01_r1\",\"n\":-3}", JsonSerializer.SerializeEvent(e));
        }

        [Test]
        public void SendTwice_OrAfterAnotherBegin_DoesNothing()
        {
            var first = EventBuilder.Begin(_collector, "a").Str("k", "1");
            var second = EventBuilder.Begin(_collector, "b").Str("k", "2");
            Assert.IsFalse(first.IsActive);
            Assert.IsFalse(first.Str("x", "y").Send(), "abandoned by the second");
            Assert.IsTrue(second.Send());
            Assert.IsFalse(second.Send(), "sent already");
            var e = Events().Single();
            Assert.AreEqual("b", e.name);
            Assert.AreEqual(new[] { "k", "2" }, e.props);
        }

        [Test]
        public void InactiveEvent_DoesNothing()
        {
            var none = default(ApmEvent);
            Assert.IsFalse(none.IsActive);
            Assert.IsFalse(none.Str("a", "b").Num("n", 1).Bool("b", true).Vec("p", 1, 2).Int("i", 3).Send());
            Assert.IsFalse(ApmClient.Event("x").Send(), "before Initialize");
            Assert.AreEqual(0, _events.Count);
        }

        // Unity runtime: an event while the SDK is not collecting allocates nothing.
        [Test]
        public void InactiveEvent_DoesNotAllocate()
        {
            // Run once before measuring: the first run of a method creates its
            // string literals ("x", "a", ...), which the runtime counts.
            TestDelegate send = () => { ApmClient.Event("x").Str("a", "b").Num("n", 1.5).Vec("p", 1, 2).Bool("b", true).Send(); };
            send();
            Assert.That(send, Is.Not.AllocatingGCMemory());
        }

        [Test]
        public void Properties_AreCopied_SoTheBufferIsReused()
        {
            Assert.IsTrue(EventBuilder.Begin(_collector, "a").Str("k", "1").Send());
            Assert.IsTrue(EventBuilder.Begin(_collector, "b").Str("k", "2").Send());
            var events = Events();
            Assert.AreEqual(new[] { "k", "1" }, events[0].props);
            Assert.AreEqual(new[] { "k", "2" }, events[1].props);
        }

        [Test]
        public void ValidateProps_WithKinds_CompactsBoth()
        {
            var pairs = new[] { "a", "1", "bad key", "2", "a", "3", "b", "true" };
            var kinds = new[] { PropKinds.Number, PropKinds.Number, PropKinds.Number, PropKinds.Bool };
            var kept = CustomCollector.ValidateProps(pairs, pairs.Length, kinds, out var keptKinds);
            Assert.AreEqual(new[] { "a", "1", "b", "true" }, kept);
            Assert.AreEqual(new[] { PropKinds.Number, PropKinds.Bool }, keptKinds);

            var all = new[] { "x", "1" };
            var allKinds = new[] { PropKinds.Number };
            Assert.AreSame(all, CustomCollector.ValidateProps(all, 2, allKinds, out var same));
            Assert.AreSame(allKinds, same);
        }

        [Test]
        public void Exceptions_CarryTheClockOfTheirFirstOccurrence()
        {
            var ex = new ExceptionCollector(new ApmConfig(), null);
            var got = new List<ExceptionEvent>();
            ex.EnqueueForTests = e => got.Add(e);
            ex.ActivateForTests();
            ex.HandleLogMessage("NullReferenceException: Object reference not set", "Room.Build ()\n", LogType.Exception);
            Assert.AreEqual(1, got.Count);
            Assert.AreEqual(2171, got[0].frame);
            Assert.AreEqual(37.25, got[0].game_t);
            StringAssert.Contains("\"t\":37.2500,\"frame\":2171", JsonSerializer.SerializeEvent(got[0]));
        }
    }
}
