using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Threading;

namespace OzxApm.Core
{
    /// <summary>
    /// A running timer (ApmClient.StartTimer, APM-6): the metric name and the
    /// start time. A value: copying or keeping it costs nothing, and nothing
    /// is allocated to start or stop it. Stop it with ApmClient.StopTimer.
    /// </summary>
    public readonly struct ApmTimer
    {
        internal readonly string Name;
        internal readonly long StartTicks;
        internal readonly int PauseEpoch;

        internal ApmTimer(string name, long startTicks, int pauseEpoch)
        {
            Name = name;
            StartTicks = startTicks;
            PauseEpoch = pauseEpoch;
        }

        /// <summary>False for default(ApmTimer).</summary>
        public bool IsValid => Name != null;

        /// <summary>The metric the elapsed time is recorded as.</summary>
        public string MetricName => Name;

        /// <summary>Milliseconds since the timer was started (0 if invalid).</summary>
        public double ElapsedMs => IsValid ? TimerRegistry.TicksToMs(Stopwatch.GetTimestamp() - StartTicks) : 0;
    }

    /// <summary>
    /// Named timers (StopTimer(name)) and the pause epoch: a timer that spans
    /// a pause of the app is not recorded (its time would include the time in
    /// background). Thread-safe; no allocation once a name has been used.
    /// </summary>
    internal static class TimerRegistry
    {
        /// <summary>Names with a pending StartTimer at once; further names are not tracked by name.</summary>
        internal const int MaxNamedTimers = 256;

        private static readonly object s_lock = new object();
        private static readonly Dictionary<string, ApmTimer> s_named = new Dictionary<string, ApmTimer>(StringComparer.Ordinal);
        private static int s_pauseEpoch;
        private static long s_spanningPause;
        private static long s_untracked;

        internal static int PauseEpoch => Volatile.Read(ref s_pauseEpoch);

        /// <summary>Timers not recorded because the app was paused while they ran.</summary>
        internal static long SpanningPauseCount => Interlocked.Read(ref s_spanningPause);

        /// <summary>StartTimer calls whose name could not be remembered (more than MaxNamedTimers pending names).</summary>
        internal static long UntrackedCount => Interlocked.Read(ref s_untracked);

        internal static double TicksToMs(long ticks) => ticks * 1000.0 / Stopwatch.Frequency;

        /// <summary>The app goes to background: running timers will not be recorded.</summary>
        internal static void OnPause() => Interlocked.Increment(ref s_pauseEpoch);

        internal static void CountSpanningPause() => Interlocked.Increment(ref s_spanningPause);

        /// <summary>Starts a timer and remembers it under its name (the latest start wins).</summary>
        internal static ApmTimer Start(string name)
        {
            var timer = new ApmTimer(name, Stopwatch.GetTimestamp(), PauseEpoch);
            if (name == null)
                return timer;
            lock (s_lock)
            {
                if (s_named.Count < MaxNamedTimers || s_named.ContainsKey(name))
                    s_named[name] = timer;
                else
                    Interlocked.Increment(ref s_untracked);
            }
            return timer;
        }

        /// <summary>Takes the pending timer of a name.</summary>
        internal static bool TryTake(string name, out ApmTimer timer)
        {
            if (name == null)
            {
                timer = default;
                return false;
            }
            lock (s_lock)
            {
                if (!s_named.TryGetValue(name, out timer))
                    return false;
                s_named.Remove(name);
                return true;
            }
        }

        /// <summary>Forgets the named entry of a timer stopped by its handle (if it is still that one).</summary>
        internal static void Forget(ApmTimer timer)
        {
            if (timer.Name == null)
                return;
            lock (s_lock)
            {
                if (s_named.TryGetValue(timer.Name, out var pending) && pending.StartTicks == timer.StartTicks)
                    s_named.Remove(timer.Name);
            }
        }

        internal static bool Cancel(string name)
        {
            if (name == null)
                return false;
            lock (s_lock)
                return s_named.Remove(name);
        }

        internal static void ResetForTests()
        {
            lock (s_lock)
                s_named.Clear();
            s_spanningPause = 0;
            s_untracked = 0;
        }
    }
}
