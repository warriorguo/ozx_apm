using System;
using System.Collections.Generic;
using System.Linq;
using NUnit.Framework;
using UnityEngine.TestTools.Constraints;
using OzxApm.Core;
using OzxApm.Collectors;
using OzxApm.Models;
using Is = UnityEngine.TestTools.Constraints.Is;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-21: the jank definition (JankTracker) and the detector around it
    /// (pause, focus, scene loads, target frame rate, budget, context). Frame
    /// times are fed by the test; nothing here needs the engine except the
    /// allocation test.
    /// </summary>
    [TestFixture]
    public class JankDetectorTests
    {
        private const float Fps30 = 33.4f;
        private const float Fps60 = 16.7f;

        private List<JankEpisode> _found;

        [SetUp]
        public void SetUp()
        {
            _found = new List<JankEpisode>();
        }

        // ---- Helpers ----

        private void Feed(JankTracker t, float ms, int frames = 1)
        {
            for (int i = 0; i < frames; i++)
            {
                if (t.Frame(ms, out JankEpisode e))
                    _found.Add(e);
            }
        }

        private void FeedFor(JankTracker t, float ms, float totalMs)
        {
            Feed(t, ms, (int)Math.Ceiling(totalMs / ms));
        }

        /// <summary>A tracker past its start grace period and warm-up at the given frame time.</summary>
        private JankTracker Warm(float frameMs)
        {
            var t = new JankTracker(50f);
            FeedFor(t, frameMs, 2000f);
            Assert.IsFalse(t.WarmingUp);
            Assert.AreEqual(0, _found.Count, "no jank while warming up");
            return t;
        }

        /// <summary>
        /// 30 fps on a 90 Hz display, as on the A33: frames of 33.3-33.5 ms,
        /// now and then one vsync late (44.4 ms) followed by an early one (22.2 ms).
        /// </summary>
        private void Steady30(JankTracker t, float seconds, Random rnd)
        {
            double left = seconds * 1000.0;
            while (left > 0)
            {
                float ms;
                int r = rnd.Next(100);
                if (r < 3)
                {
                    Feed(t, 44.4f);
                    ms = 22.2f;
                    left -= 44.4f;
                }
                else
                {
                    ms = 33.3f + (float)rnd.NextDouble() * 0.2f;
                }
                Feed(t, ms);
                left -= ms;
            }
        }

        // ---- Defaults ----

        [Test]
        public void Config_Defaults()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(50f, config.JankThresholdMs);
            Assert.AreEqual(20, config.MaxJanksPerMinute);
            var custom = new ApmConfig { JankThresholdMs = 100f };
            Assert.AreEqual(100f, custom.JankThresholdMs);
        }

        [Test]
        public void SlowThreshold_IsRelativeToTheExpectedFrameTime()
        {
            Assert.AreEqual(50.1f, JankTracker.SlowThreshold(33.4f), 0.01f);   // 30 fps: 1.5 x
            Assert.AreEqual(25.05f, JankTracker.SlowThreshold(16.7f), 0.01f);  // 60 fps: 1.5 x
            Assert.AreEqual(19.1f, JankTracker.SlowThreshold(11.1f), 0.01f);   // 90 fps: + 8 ms
            Assert.AreEqual(16.3f, JankTracker.SlowThreshold(8.3f), 0.01f);    // 120 fps: + 8 ms
            Assert.AreEqual(0f, JankTracker.SlowThreshold(0f));
        }

        // ---- The bug: a 30 fps game never ended a jank ----

        [Test]
        public void Steady30Fps_FiveMinutes_NoJank()
        {
            var t = Warm(Fps30);
            Steady30(t, 300, new Random(21));
            Assert.AreEqual(0, _found.Count);
            Assert.AreEqual(33.4f, t.ExpectedFrameMs, 0.8f); // an average: one 44.4 ms frame moves it 0.7 ms
        }

        [Test]
        public void SingleLongFrame_At30Fps_IsOneJank_WithItsOwnTime()
        {
            var t = Warm(Fps30);
            Feed(t, 150f);
            Assert.IsTrue(t.InJank, "the long frame itself starts the jank");
            Steady30(t, 10, new Random(1));
            Assert.AreEqual(1, _found.Count);
            var e = _found[0];
            Assert.AreEqual(150f, e.DurationMs, 0.001f);
            Assert.AreEqual(150f, e.MaxFrameMs, 0.001f);
            Assert.AreEqual(1, e.SlowFrames);
            Assert.AreEqual(1, e.Frames);
            Assert.AreEqual(33.4f, e.ExpectedFrameMs, 0.15f);
            Assert.IsFalse(e.Capped);
        }

        [Test]
        public void SingleLongFrame_IsReportedAfterTheMergeGap()
        {
            var t = Warm(Fps30);
            Feed(t, 200f);
            Feed(t, Fps30, 7); // 233.8 ms of normal frames
            Assert.AreEqual(0, _found.Count);
            Feed(t, Fps30); // 267.2 ms >= 250
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(200f, _found[0].DurationMs, 0.001f);
        }

        [Test]
        public void SingleLongFrame_At60Fps_IsOneJank()
        {
            var t = Warm(Fps60);
            Feed(t, 150f);
            FeedFor(t, Fps60, 5000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(150f, _found[0].DurationMs, 0.001f);
            Assert.AreEqual(1, _found[0].Frames);
        }

        [Test]
        public void ConsecutiveLongFrames_AreOneJank()
        {
            var t = Warm(Fps30);
            Feed(t, 120f, 3);
            FeedFor(t, Fps30, 2000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(360f, _found[0].DurationMs, 0.01f);
            Assert.AreEqual(120f, _found[0].MaxFrameMs, 0.001f);
            Assert.AreEqual(3, _found[0].SlowFrames);
            Assert.AreEqual(3, _found[0].Frames);
        }

        [Test]
        public void LongFramesWithinTheMergeGap_AreOneJank_SpanIncludesTheNormalFrames()
        {
            var t = Warm(Fps30);
            Feed(t, 120f);
            Feed(t, Fps30, 2);
            Feed(t, 90f);
            FeedFor(t, Fps30, 2000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(120f + 2 * Fps30 + 90f, _found[0].DurationMs, 0.01f);
            Assert.AreEqual(2, _found[0].SlowFrames);
            Assert.AreEqual(4, _found[0].Frames);
        }

        [Test]
        public void LongFramesASecondApart_AreTwoJanks()
        {
            var t = Warm(Fps30);
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(2, _found.Count);
        }

        [Test]
        public void WaveJank_120MsEvery5s_At30Fps_EveryOneIsAJank()
        {
            // APM-9's wavejank=120 produced 0 janks on the A33.
            var t = Warm(Fps30);
            var rnd = new Random(9);
            for (int i = 0; i < 24; i++)
            {
                Steady30(t, 5, rnd);
                Feed(t, 120f);
            }
            Steady30(t, 1, rnd);
            Assert.AreEqual(24, _found.Count);
            Assert.IsTrue(_found.All(e => e.DurationMs == 120f && e.Frames == 1));
        }

        // ---- What is not a jank ----

        [Test]
        public void FramesBelowTheSlowThreshold_AreNotJanks()
        {
            var t = Warm(Fps30);
            Feed(t, 50f);   // 30 fps: slow means > 50.1
            Feed(t, Fps30, 20);
            Feed(t, 44.4f, 3); // one 90 Hz vsync late, three times
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(0, _found.Count);
        }

        [Test]
        public void At60Fps_ASingle45MsFrameIsNot_TwoConsecutive30MsFramesAre()
        {
            var t = Warm(Fps60);
            Feed(t, 45f);           // slow, but alone under 50 ms
            FeedFor(t, Fps60, 1000f);
            Feed(t, 30f);           // slow, alone 30 ms
            FeedFor(t, Fps60, 1000f);
            Assert.AreEqual(0, _found.Count);
            Feed(t, 30f, 2);        // two in a row: 60 ms
            FeedFor(t, Fps60, 1000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(60f, _found[0].DurationMs, 0.001f);
            Assert.AreEqual(2, _found[0].SlowFrames);
        }

        [Test]
        public void AGameRunningAt20Fps_IsNotJanky()
        {
            var t = Warm(50f);
            var rnd = new Random(3);
            for (int i = 0; i < 3000; i++)
                Feed(t, 45f + (float)rnd.NextDouble() * 15f);
            Assert.AreEqual(0, _found.Count);
        }

        [Test]
        public void ModerateSlowdown_BecomesTheBaseline()
        {
            // 30 -> 23 fps (+10 ms per frame): under 1.5 x, not a jank.
            var t = Warm(Fps30);
            FeedFor(t, 43.4f, 10000f);
            Assert.AreEqual(0, _found.Count);
            Assert.AreEqual(43.4f, t.ExpectedFrameMs, 0.01f);
        }

        // ---- Sustained slowdowns ----

        [Test]
        public void SustainedSlowdown_ThreeSeconds_At60Fps_IsOneJank()
        {
            var t = Warm(Fps60);
            Feed(t, 36.7f, 82);    // +20 ms per frame for ~3 s
            FeedFor(t, Fps60, 2000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(82 * 36.7f, _found[0].DurationMs, 0.5f);
            Assert.AreEqual(82, _found[0].SlowFrames);
            Assert.IsFalse(_found[0].Capped);
            Assert.AreEqual(Fps60, t.ExpectedFrameMs, 0.01f, "the slow frames did not enter the baseline");
        }

        [Test]
        public void SustainedSlowdown_ThreeSeconds_At30Fps_IsOneJank()
        {
            var t = Warm(Fps30);
            Feed(t, 73.4f, 41);    // +40 ms per frame for ~3 s
            FeedFor(t, Fps30, 2000f);
            Assert.AreEqual(1, _found.Count);
            Assert.AreEqual(41 * 73.4f, _found[0].DurationMs, 0.5f);
            Assert.AreEqual(41, _found[0].SlowFrames);
        }

        [Test]
        public void LastingDrop_IsOneCappedJank_ThenTheNewRateIsTheBaseline()
        {
            // 60 -> 30 fps without a target change (e.g. thermal throttling).
            var t = Warm(Fps60);
            FeedFor(t, 33.3f, 20000f);
            Assert.AreEqual(1, _found.Count);
            Assert.IsTrue(_found[0].Capped);
            Assert.GreaterOrEqual(_found[0].DurationMs, JankTracker.MaxEpisodeMs);
            Assert.Less(_found[0].DurationMs, JankTracker.MaxEpisodeMs + 33.4f);
            Assert.AreEqual(33.3f, t.ExpectedFrameMs, 0.01f);
            // And back to 60 fps: faster frames are never janks.
            FeedFor(t, Fps60, 5000f);
            Assert.AreEqual(1, _found.Count);
        }

        [Test]
        public void OneVeryLongFrame_IsOneJank_AndKeepsTheBaseline()
        {
            var t = Warm(Fps30);
            Feed(t, 6000f);
            Assert.AreEqual(1, _found.Count, "reported at once: it reached MaxEpisodeMs");
            Assert.AreEqual(6000f, _found[0].DurationMs);
            Assert.IsFalse(t.WarmingUp);
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(2, _found.Count);
        }

        // ---- Start, pause, resume, scene loads, target changes ----

        [Test]
        public void StartupFrames_AreNotJanks()
        {
            var t = new JankTracker(50f);
            Feed(t, 0f);          // Unity's first frame
            Feed(t, 2400f);       // the first frame after loading
            Feed(t, 400f, 2);
            Feed(t, 120f, 5);
            FeedFor(t, Fps30, 3000f);
            Assert.AreEqual(0, _found.Count);
            Assert.AreEqual(Fps30, t.ExpectedFrameMs, 0.01f, "long warm-up frames leave the baseline");
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(1, _found.Count);
        }

        [Test]
        public void ResumeAfterBackground_IsNotAJank()
        {
            // APM-6 device test: Home for 2-6 s, then a "jank" of 20 s with a 6210 ms frame.
            var t = Warm(Fps30);
            Assert.IsFalse(t.Flush(out _));
            t.Suspend();
            Feed(t, Fps30, 10);    // frames while paused (none on a device) are ignored
            t.Resume();
            Feed(t, 6210f);
            Feed(t, 80f);          // resume work
            Feed(t, 60f);
            Steady30(t, 30, new Random(5));
            Assert.AreEqual(0, _found.Count);
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(1, _found.Count, "detection works again after the grace period");
        }

        [Test]
        public void JankInProgress_IsFlushedWithItsOwnFrames()
        {
            var t = Warm(Fps30);
            Feed(t, 150f);
            Feed(t, Fps30);
            Assert.IsTrue(t.Flush(out JankEpisode e));
            Assert.AreEqual(150f, e.DurationMs);
            Assert.AreEqual(1, e.Frames);
            Assert.IsFalse(t.Flush(out _), "flushed once");
        }

        [Test]
        public void ShortRunInProgress_IsNotFlushedAsAJank()
        {
            var t = Warm(Fps60);
            Feed(t, 30f);
            Assert.IsFalse(t.Flush(out _));
        }

        [Test]
        public void SceneLoadFrames_AreNotJanks()
        {
            var t = Warm(Fps30);
            t.SkipSceneLoad();
            Feed(t, 3000f);
            Feed(t, 200f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(0, _found.Count);
            Feed(t, 150f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(1, _found.Count, "the frames after the two skipped ones are watched");
        }

        [Test]
        public void InvalidFrameTimes_AreIgnored()
        {
            var t = Warm(Fps30);
            Feed(t, float.NaN);
            Feed(t, float.PositiveInfinity);
            Feed(t, -5f);
            Feed(t, 0f);
            FeedFor(t, Fps30, 1000f);
            Assert.AreEqual(0, _found.Count);
            Assert.AreEqual(Fps30, t.ExpectedFrameMs, 0.01f);
        }

        [Test]
        public void Baseline_WarmUpMedian_ThenTheAverageOfNormalFrames()
        {
            var t = Warm(20f);
            Assert.AreEqual(20f, t.ExpectedFrameMs);
            for (int i = 0; i < 200; i++)
                Feed(t, i % 2 == 0 ? 16f : 17f);
            Assert.AreEqual(16.5f, t.ExpectedFrameMs, 0.05f, "the average of the normal frames");
            Feed(t, 30f);  // slow (> 24.5): not taken in
            Assert.AreEqual(16.5f, t.ExpectedFrameMs, 0.05f);
            t.ResetBaseline();
            Assert.IsTrue(t.WarmingUp);
            Assert.AreEqual(0f, t.ExpectedFrameMs);
            // Warm-up: the median of 15 frames, whatever they are (taken in without detection).
            Feed(t, 900f, 4);
            Feed(t, 200f, JankTracker.WarmupFrames - 4);
            Assert.AreEqual(0, _found.Count);
            Assert.IsFalse(t.WarmingUp);
            Assert.AreEqual(200f, t.ExpectedFrameMs);
        }

        [Test]
        public void Budget_AllowsMaxPerMinute()
        {
            var b = new JankBudget();
            for (int i = 0; i < 3; i++)
                Assert.IsTrue(b.TryTake(1000 + i, 3));
            Assert.IsFalse(b.TryTake(2000, 3));
            Assert.IsFalse(b.TryTake(60999, 3));
            Assert.IsTrue(b.TryTake(61000, 3), "60 s after the first");
            Assert.IsTrue(b.TryTake(61001, 3), "60 s after the second");
            Assert.IsFalse(b.TryTake(61001.5, 3), "the third was sent at 1002");
            Assert.IsTrue(b.TryTake(61002, 3));
            Assert.IsFalse(b.TryTake(61003, 3), "sliding: 3 in the last 60 s");
            Assert.IsFalse(b.TryTake(61004, 0));
            // Any 60 s, not calendar minutes: 25 janks 1.5 s apart -> 20 sent.
            var c = new JankBudget();
            int sent = 0;
            for (int i = 0; i < 25; i++)
                if (c.TryTake(50000 + i * 1500, 20)) sent++;
            Assert.AreEqual(20, sent);
        }

        // ---- The detector: events, context, pause, focus, target frame rate ----

        private JankDetector NewDetector(ApmConfig config = null)
        {
            var d = new JankDetector(config ?? new ApmConfig(), null)
            {
                SentForTests = new List<JankEvent>(),
                TimelineForTests = new Timeline(),
            };
            d.StartWith(-1, 0);
            return d;
        }

        private static void Frames(JankDetector d, float ms, int frames, int target = -1, int vSync = 0)
        {
            for (int i = 0; i < frames; i++)
                d.UpdateWith(ms, target, vSync);
        }

        [Test]
        public void Detector_SendsTheJank_WithItsStartTime_ContextAndRecentEvents()
        {
            GameContext.ResetForTests();
            try
            {
                var d = NewDetector();
                Frames(d, Fps30, 60);
                d.TimelineForTests.RecordText("Scene: Arena");
                GameContext.Set("roomId", "r-1");
                Frames(d, 150f, 1);
                GameContext.Set("roomId", "r-2"); // after the hitch
                Frames(d, Fps30, 9);
                long now = Timeline.NowMs();
                Assert.AreEqual(1, d.SentForTests.Count);
                var e = d.SentForTests[0];
                Assert.AreEqual("jank", e.type);
                Assert.AreEqual(150f, e.duration_ms);
                Assert.AreEqual(150f, e.max_frame_ms);
                Assert.IsTrue(e.ctx.TryGetValue("roomId", out string room));
                Assert.AreEqual("r-1", room, "the context when the jank began");
                // Noted in the frame of the long frame: the wall time then minus
                // the frame (the test's frames take no real time).
                long expected = now - 150;
                Assert.That(e.timestamp, NUnit.Framework.Is.InRange(expected - 50, expected + 50));
                Assert.IsTrue(e.recent_events.Any(l => l.EndsWith("Scene: Arena", StringComparison.Ordinal)));
                Assert.AreEqual(1, d.Detected);
                Assert.AreEqual(1, d.Sent);
                Assert.AreEqual(150f, d.LastDurationMs);
                Assert.AreEqual(1, d.LastFrames);
            }
            finally
            {
                GameContext.ResetForTests();
            }
        }

        [Test]
        public void Detector_MultiFrameJank_HasTheContextAndTimeOfItsFirstSlowFrame()
        {
            GameContext.ResetForTests();
            try
            {
                var d = NewDetector();
                Frames(d, Fps60, 60, 60);
                GameContext.Set("roomId", "r-1");
                long before = Timeline.NowMs();
                Frames(d, 30f, 1, 60);              // slow, not yet a jank
                GameContext.Set("roomId", "r-2");   // between the two slow frames
                Frames(d, 30f, 1, 60);              // now a jank (60 ms)
                d.OnSceneLoaded();                  // flushed at a scene load
                Assert.AreEqual(1, d.SentForTests.Count);
                var e = d.SentForTests[0];
                Assert.AreEqual(60f, e.duration_ms, 0.001f);
                Assert.IsTrue(e.ctx.TryGetValue("roomId", out string room));
                Assert.AreEqual("r-1", room, "the context of the first slow frame");
                Assert.That(e.timestamp, NUnit.Framework.Is.InRange(before - 30 - 50, Timeline.NowMs() - 30 + 50));
            }
            finally
            {
                GameContext.ResetForTests();
            }
        }

        [Test]
        public void Detector_Pause_SendsTheJankInProgress_AndTheResumeFrameIsNotAJank()
        {
            var d = NewDetector();
            Frames(d, Fps30, 60);
            Frames(d, 150f, 1);
            d.OnPause(true);
            Assert.AreEqual(1, d.SentForTests.Count, "sent before the pause flush");
            Assert.AreEqual(150f, d.SentForTests[0].duration_ms);
            d.OnPause(false);
            Frames(d, 6210f, 1);
            Frames(d, Fps30, 300);
            Assert.AreEqual(1, d.SentForTests.Count);
        }

        [Test]
        public void Detector_FocusLoss_IsLikeAPause()
        {
            var d = NewDetector();
            Frames(d, Fps30, 60);
            d.OnFocus(false);
            Frames(d, 4000f, 1);    // e.g. the Editor without focus
            d.OnFocus(true);
            Frames(d, 4000f, 1);
            Frames(d, Fps30, 300);
            Assert.AreEqual(0, d.SentForTests.Count);
            // Paused and unfocused: back only when both are over.
            d.OnFocus(false);
            d.OnPause(true);
            d.OnFocus(true);
            Frames(d, 3000f, 1);
            Assert.AreEqual(0, d.SentForTests.Count);
            d.OnPause(false);
            Frames(d, 3000f, 1);
            Frames(d, Fps30, 300);
            Assert.AreEqual(0, d.SentForTests.Count);
        }

        [Test]
        public void Detector_SceneLoad_SendsTheJankInProgress_AndSkipsTheLoadingFrames()
        {
            var d = NewDetector();
            Frames(d, Fps30, 60);
            Frames(d, 150f, 1);
            d.OnSceneLoaded();
            Assert.AreEqual(1, d.SentForTests.Count, "sent before the scene name changes");
            Frames(d, 2500f, 1);
            Frames(d, 300f, 1);
            Frames(d, Fps30, 60);
            Assert.AreEqual(1, d.SentForTests.Count);
        }

        [Test]
        public void Detector_TargetFrameRateChange_IsNotAJank()
        {
            var d = NewDetector();
            Frames(d, Fps60, 120, 60);
            Frames(d, 33.3f, 600, 30);   // fps=30
            Frames(d, Fps60, 600, 60);   // fps=60
            Frames(d, Fps60, 600, 60, 1);
            Assert.AreEqual(0, d.SentForTests.Count);
            Frames(d, 150f, 1, 60, 1);
            Frames(d, Fps60, 60, 60, 1);
            Assert.AreEqual(1, d.SentForTests.Count);
        }

        // As in Update: the frame rate settings are read once a second, not every frame.
        private static void FramesPolled(JankDetector d, float ms, int frames, int target, ref float sincePoll)
        {
            for (int i = 0; i < frames; i++)
            {
                sincePoll += ms;
                if (sincePoll >= JankDetector.PollIntervalMs)
                {
                    sincePoll = 0f;
                    d.PollFrameRate(target, 0);
                }
                d.Feed(ms);
            }
        }

        [Test]
        public void Detector_TargetChangeSeenUpToASecondLate_IsNotAJank()
        {
            var d = NewDetector();
            float since = 0f;
            FramesPolled(d, Fps60, 300, 60, ref since);
            FramesPolled(d, 33.3f, 600, 30, ref since);   // slow until the next reading
            FramesPolled(d, Fps60, 600, 60, ref since);
            Assert.AreEqual(0, d.SentForTests.Count);
            Assert.AreEqual(0, d.Detected);
        }

        [Test]
        public void Detector_JankBeforeTheLastReading_IsSentAtATargetChange()
        {
            var d = NewDetector();
            float since = 0f;
            FramesPolled(d, Fps60, 300, 60, ref since);
            since = 0f;
            d.PollFrameRate(60, 0);                        // a reading
            FramesPolled(d, 30f, 3, 60, ref since);        // a jank begins after it...
            d.PollFrameRate(30, 0);                        // ...and the target changes: dropped
            Assert.AreEqual(0, d.SentForTests.Count);
            FramesPolled(d, 33.3f, 120, 30, ref since);
            d.PollFrameRate(30, 0);
            d.Feed(150f);                                  // a jank before the reading below
            d.Feed(33.3f);
            d.PollFrameRate(30, 0);
            d.Feed(33.3f);
            d.PollFrameRate(60, 0);                        // change: the jank began before the previous reading
            Assert.AreEqual(1, d.SentForTests.Count);
            Assert.AreEqual(150f, d.SentForTests[0].duration_ms);
        }

        [Test]
        public void TargetFrameMs_FromTargetFrameRate()
        {
            Assert.AreEqual(0f, JankDetector.TargetFrameMs(-1));
            Assert.AreEqual(0f, JankDetector.TargetFrameMs(0));
            Assert.AreEqual(1000f / 60f, JankDetector.TargetFrameMs(60), 0.001f);
            Assert.AreEqual(1000f / 30f, JankDetector.TargetFrameMs(30), 0.001f);
            Assert.AreEqual(0f, JankDetector.TargetFrameMs(5000));
        }

        [Test]
        public void Detector_60FpsOn90HzDisplay_AlternatingRefreshes_IsNotJanky()
        {
            // targetFrameRate 60 on a 90 Hz display: frames of 1 and 2 refreshes
            // (11.1 / 22.2 ms), sometimes three long ones in a row (66.6 ms). The
            // median alone (11.1) would make 22.2 slow and three of them a jank;
            // the target frame time (16.7) is the floor.
            var d = NewDetector();
            var rnd = new Random(90);
            Frames(d, 16.7f, 60, 60);
            for (int i = 0; i < 18000; i++)
            {
                int r = rnd.Next(100);
                if (r < 2)
                    Frames(d, 22.2f, 3, 60);
                else
                    Frames(d, i % 2 == 0 ? 11.1f : 22.2f, 1, 60);
            }
            Assert.AreEqual(0, d.SentForTests.Count);
            Assert.GreaterOrEqual(d.ExpectedFrameMs, 1000f / 60f);
            Frames(d, 150f, 1, 60);
            Frames(d, 11.1f, 30, 60);
            Assert.AreEqual(1, d.SentForTests.Count);
        }

        [Test]
        public void Detector_BeyondMaxJanksPerMinute_CountsAndDrops()
        {
            var d = NewDetector(new ApmConfig { MaxJanksPerMinute = 3 });
            Frames(d, Fps30, 60);
            for (int i = 0; i < 5; i++)
            {
                Frames(d, 150f, 1);
                Frames(d, Fps30, 30);
            }
            Assert.AreEqual(5, d.Detected);
            Assert.AreEqual(3, d.Sent);
            Assert.AreEqual(2, d.Dropped);
            Assert.AreEqual(3, d.SentForTests.Count);
            Frames(d, Fps30, 1800);  // a minute later
            Frames(d, 150f, 1);
            Frames(d, Fps30, 30);
            Assert.AreEqual(4, d.SentForTests.Count);
        }

        [Test]
        public void Detector_Stop_SendsTheJankInProgress()
        {
            var d = NewDetector();
            Frames(d, Fps30, 60);
            Frames(d, 150f, 1);
            d.Stop();
            Assert.AreEqual(1, d.SentForTests.Count);
            Assert.IsFalse(d.IsActive);
            Frames(d, 150f, 1);
            d.OnPause(true);
            Assert.AreEqual(1, d.SentForTests.Count);
        }

        // The detector runs every frame: no allocation, also not while a jank goes on.
        [Test]
        public void Detector_PerFrame_DoesNotAllocate()
        {
            var d = NewDetector(new ApmConfig { MaxJanksPerMinute = 0 }); // detected, not sent
            Frames(d, Fps30, 60);
            // Every path once (a first run can allocate for the runtime, not the SDK).
            Frames(d, 80f, 1);
            Frames(d, Fps30, 30);
            Assert.AreEqual(1, d.Detected);
            // Statement lambdas (an expression lambda returning a value is an
            // ActualValueDelegate, which the constraint refuses), each run once
            // before it is measured.
            TestDelegate normal = () => { d.UpdateWith(Fps30, -1, 0); };
            TestDelegate slow = () => { d.UpdateWith(80f, -1, 0); };
            normal(); slow(); normal();
            Assert.That(normal, Is.Not.AllocatingGCMemory());
            Assert.That(slow, Is.Not.AllocatingGCMemory());
            Assert.That(normal, Is.Not.AllocatingGCMemory());
            var t = Warm(Fps30);
            TestDelegate trackerNormal = () => { t.Frame(Fps30, out _); };
            TestDelegate trackerSlow = () => { t.Frame(90f, out _); };
            trackerNormal(); trackerSlow(); trackerNormal();
            Assert.That(trackerNormal, Is.Not.AllocatingGCMemory());
            Assert.That(trackerSlow, Is.Not.AllocatingGCMemory());
        }
    }
}
