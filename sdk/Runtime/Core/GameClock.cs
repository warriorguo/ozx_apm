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
    /// Unity's Time API may only be read on the main thread. On the main thread
    /// the values are read live (they are constant within a frame); on any
    /// other thread they are those of the last frame the SDK saw
    /// (<see cref="Tick"/>, every frame, from ApmClient.Update), which is the
    /// frame running while that thread records. Before the first frame (or
    /// outside Unity) there is no value and events carry none.
    ///
    /// Cost: two engine properties on the main thread, an uncontended lock
    /// elsewhere; nothing is allocated.
    /// </summary>
    internal static class GameClock
    {
        private static readonly object s_lock = new object();
        private static int s_mainThreadId = -1;
        private static double s_time;
        private static int s_frame = -1;

        // Test seams: replace the engine (tests run outside Unity too).
        internal static Func<double> TimeForTests;
        internal static Func<int> FrameForTests;

        /// <summary>Main thread (ApmClient.Initialize): remembers it and reads the clock.</summary>
        internal static void Init()
        {
            Volatile.Write(ref s_mainThreadId, Environment.CurrentManagedThreadId);
            Tick();
        }

        /// <summary>Main thread, every frame: the values other threads read.</summary>
        internal static void Tick()
        {
            if (!ReadLive(out double t, out int frame))
                return;
            lock (s_lock)
            {
                s_time = t;
                s_frame = frame;
            }
        }

        /// <summary>
        /// The game time and frame now (see the class comment); false when
        /// there is none yet. Any thread.
        /// </summary>
        internal static bool Now(out double t, out int frame)
        {
            if (Environment.CurrentManagedThreadId == Volatile.Read(ref s_mainThreadId) && ReadLive(out t, out frame))
                return true;
            lock (s_lock)
            {
                t = s_time;
                frame = s_frame;
            }
            return frame >= 0;
        }

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
            Volatile.Write(ref s_mainThreadId, -1);
            lock (s_lock)
            {
                s_time = 0;
                s_frame = -1;
            }
        }
    }
}
