using System;
using System.Runtime.CompilerServices;
using System.Threading;
using UnityEngine;

namespace OzxApm.Core
{
    /// <summary>
    /// The game's own clock for the events (APM-10): <c>t</c>, Unity's scaled
    /// game time in seconds (<c>Time.timeAsDouble</c>, the clock OZX's
    /// AiTelemetry lines use: it stops while the game is paused or
    /// timeScale is 0, and does not advance in the background), and
    /// <c>frame</c>, <c>Time.frameCount</c>. Every event the SDK records while
    /// the game runs carries both, as the client saw them; the server never
    /// derives them.
    ///
    /// Nothing runs per frame. Unity's Time API may only be read on the main
    /// thread: there the two values are read when an event is created (they
    /// are constant within a frame) and published for other threads. On any
    /// other thread an event gets the last published pair: from the main
    /// thread's last event, at the latest the last perf sample (one every
    /// ApmConfig.SamplingIntervalSeconds, stamped on the main thread), so it
    /// is at most that interval old (more only when perf sampling is off and
    /// no event was recorded on the main thread since). Before the first
    /// main-thread event (or outside Unity) there is none.
    ///
    /// The pair is published lock-free (one writer, the main thread; a
    /// sequence counter so readers never see half of one update and half of
    /// another). Cost: two engine properties per main-thread event, nothing
    /// allocated.
    /// </summary>
    internal static class GameClock
    {
        private static int s_mainThreadId = -1;
        // Published pair, written by the main thread only. s_version is odd
        // while a write is in progress.
        private static int s_version;
        private static double s_time;
        private static int s_frame = -1;

        // Test seams: replace the engine (tests run outside Unity too).
        internal static Func<double> TimeForTests;
        internal static Func<int> FrameForTests;

        /// <summary>Main thread (ApmClient.Initialize): remembers it and publishes the clock.</summary>
        internal static void Init()
        {
            Volatile.Write(ref s_mainThreadId, Environment.CurrentManagedThreadId);
            Refresh();
        }

        /// <summary>Main thread: reads the clock and publishes it for other threads.</summary>
        internal static void Refresh()
        {
            if (Environment.CurrentManagedThreadId == Volatile.Read(ref s_mainThreadId))
                Now(out _, out _);
        }

        /// <summary>
        /// The game time and frame now (see the class comment); false when
        /// there is none yet. Any thread; on the main thread it also publishes
        /// the values for the other threads.
        /// </summary>
        internal static bool Now(out double t, out int frame)
        {
            if (Environment.CurrentManagedThreadId == Volatile.Read(ref s_mainThreadId) && ReadLive(out t, out frame))
            {
                Publish(t, frame);
                return true;
            }
            return ReadPublished(out t, out frame);
        }

        /// <summary>
        /// Main thread: the clock of the frame before this one, i.e. the frame
        /// whose duration this frame's Time.deltaTime reports (a jank's first
        /// slow frame, APM-21: its start is Time.time - Time.deltaTime, its
        /// number frameCount - 1). False off the main thread or without a clock.
        /// </summary>
        internal static bool PreviousFrame(out double t, out int frame)
        {
            if (Environment.CurrentManagedThreadId != Volatile.Read(ref s_mainThreadId) || !Now(out t, out frame))
            {
                t = 0;
                frame = -1;
                return false;
            }
            double delta;
            var testDelta = DeltaForTests;
            if (testDelta != null)
            {
                delta = testDelta();
            }
            else
            {
                try
                {
                    delta = ReadEngineDelta();
                }
                catch
                {
                    delta = 0;
                }
            }
            t = Math.Max(0, t - (delta > 0 ? delta : 0));
            frame = Math.Max(0, frame - 1);
            return true;
        }

        internal static Func<double> DeltaForTests;

        [MethodImpl(MethodImplOptions.NoInlining)]
        private static double ReadEngineDelta() => Time.deltaTime;

        /// <summary>Stamps evt with the game time and frame now, unless it has them or there are none.</summary>
        internal static void Stamp(OzxApm.Models.BaseEvent evt)
        {
            if (evt == null || evt.frame >= 0)
                return;
            if (Now(out double t, out int frame))
            {
                evt.game_t = t;
                evt.frame = frame;
            }
        }

        // Single writer (the main thread).
        private static void Publish(double t, int frame)
        {
            if (Volatile.Read(ref s_frame) == frame && Volatile.Read(ref s_time) == t)
                return; // another event of the same frame
            int v = s_version;
            Volatile.Write(ref s_version, v + 1); // odd: writing
            Interlocked.MemoryBarrier();
            Volatile.Write(ref s_time, t);
            Volatile.Write(ref s_frame, frame);
            Volatile.Write(ref s_version, v + 2);
        }

        private static bool ReadPublished(out double t, out int frame)
        {
            for (int attempt = 0; ; attempt++)
            {
                int v1 = Volatile.Read(ref s_version);
                t = Volatile.Read(ref s_time);
                frame = Volatile.Read(ref s_frame);
                Interlocked.MemoryBarrier();
                int v2 = Volatile.Read(ref s_version);
                if (v1 == v2 && (v1 & 1) == 0)
                    return frame >= 0;
                if (attempt >= 100)
                {
                    // The writer is preempted mid-update for that long: no clock
                    // rather than a torn pair.
                    t = 0;
                    frame = -1;
                    return false;
                }
                Thread.SpinWait(4);
            }
        }

        private static bool ReadLive(out double t, out int frame)
        {
            var testTime = TimeForTests;
            if (testTime != null)
            {
                t = testTime();
                var testFrame = FrameForTests;
                frame = testFrame != null ? testFrame() : 0;
                return frame >= 0 && t >= 0;
            }
            try
            {
                ReadEngine(out t, out frame);
            }
            catch
            {
                // Unity refused the API here (a restricted callback): no clock,
                // never an exception into the caller (the log callback, TrackEvent).
                t = 0;
                frame = -1;
                return false;
            }
            return frame >= 0 && t >= 0;
        }

        // Its own method: outside Unity (tests on plain Mono) it is never compiled.
        [MethodImpl(MethodImplOptions.NoInlining)]
        private static void ReadEngine(out double t, out int frame)
        {
#if UNITY_2020_2_OR_NEWER
            t = Time.timeAsDouble;
#else
            t = Time.time;
#endif
            frame = Time.frameCount;
        }

        internal static void ResetForTests()
        {
            TimeForTests = null;
            FrameForTests = null;
            DeltaForTests = null;
            Volatile.Write(ref s_mainThreadId, -1);
            Volatile.Write(ref s_version, 0);
            Volatile.Write(ref s_time, 0);
            Volatile.Write(ref s_frame, -1);
        }
    }
}
