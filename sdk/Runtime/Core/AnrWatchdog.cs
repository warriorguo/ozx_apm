using System;

namespace OzxApm.Core
{
    /// <summary>
    /// The Java main (UI) thread, as the ANR watchdog sees it (Android:
    /// AndroidMainThreadProbe; tests: a fake). Every method is called on the
    /// watchdog's thread and never throws.
    /// </summary>
    internal interface IMainThreadProbe
    {
        /// <summary>Prepares the probe on the watchdog's thread (JVM attach, handler); false: unusable.</summary>
        bool Start();
        /// <summary>Releases what Start took (on the watchdog's thread).</summary>
        void Stop();
        /// <summary>Queues the marker message on the main thread's looper.</summary>
        bool Post();
        /// <summary>The marker is still queued: the main thread has not got to it.</summary>
        bool IsPending();
        /// <summary>Removes the marker (the game was paused).</summary>
        void Cancel();
        /// <summary>Android has this process in the NOT_RESPONDING state (ActivityManager.getProcessesInErrorState); message: its short message.</summary>
        bool IsNotResponding(out string message);
        /// <summary>The main thread's Java stack in the normalized form ("Thread "main" tid=N crashed", "at ..." lines); "" if unknown.</summary>
        string MainThreadStack(int maxFrames);
    }

    /// <summary>
    /// Detects an ANR while the process lives (APM-7): most ANRs do not end
    /// the process (the A33 showed no dialog and did not kill it), and many
    /// players do not relaunch soon. Every TickMs on its own thread, in the
    /// foreground only: a marker message is queued on the main looper (one
    /// JNI call); while it is still queued BlockedMs later, ActivityManager's
    /// error state of the process is polled (only then); when Android has
    /// declared the process NOT_RESPONDING, the ANR is reported once for that
    /// block, with the main thread's Java stack. A main thread that is merely
    /// slow (no input waiting, so no ANR for Android) is not reported.
    ///
    /// Cost: one or two JNI calls per second on a background thread, no
    /// managed allocation per check; the error-state poll and the stack only
    /// while the main thread is blocked for more than BlockedMs.
    /// </summary>
    internal sealed class AnrWatchdog
    {
        internal const int TickMs = 1000;
        /// <summary>The marker queued this long: the main thread is blocked (Android's input timeout is 5 s).</summary>
        internal const long BlockedMs = 5000;
        /// <summary>A block is checked against Android's error state for at most this long.</summary>
        internal const long ConfirmForMs = 30000;
        /// <summary>The next marker this long after the previous one ran.</summary>
        internal const long IntervalMs = 2000;
        /// <summary>ANRs reported live per process (a process stuck in a loop of ANRs).</summary>
        internal const int MaxReportsPerProcess = 3;

        private readonly IMainThreadProbe _probe;
        private readonly Func<bool> _active;
        private readonly Action<string, string> _onAnr;
        private bool _pending;
        private long _sentAt;
        private long _nextPostAt;
        private bool _blockDone;
        private bool _blockCounted;

        /// <summary>Blocks of more than BlockedMs, and of those, ANRs Android confirmed (stats, tests).</summary>
        internal int Blocks, Reported;

        /// <param name="active">Watch now (foreground, collecting).</param>
        /// <param name="onAnr">An ANR: Android's message and the main thread's stack (on the watchdog's thread).</param>
        internal AnrWatchdog(IMainThreadProbe probe, Func<bool> active, Action<string, string> onAnr)
        {
            _probe = probe;
            _active = active;
            _onAnr = onAnr;
        }

        /// <summary>One check; nowMs is a monotonic clock (ms).</summary>
        internal void Tick(long nowMs)
        {
            if (!_active())
            {
                if (_pending)
                {
                    _probe.Cancel();
                    _pending = false;
                }
                _nextPostAt = 0;
                return;
            }
            if (!_pending)
            {
                if (nowMs < _nextPostAt)
                    return;
                if (_probe.Post())
                {
                    _pending = true;
                    _sentAt = nowMs;
                    _blockDone = false;
                    _blockCounted = false;
                }
                return;
            }
            if (!_probe.IsPending())
            {
                _pending = false; // the main thread ran it
                _nextPostAt = nowMs + IntervalMs;
                return;
            }
            long blocked = nowMs - _sentAt;
            if (blocked < BlockedMs || _blockDone)
                return;
            if (!_blockCounted)
            {
                _blockCounted = true;
                Blocks++;
            }
            if (blocked > BlockedMs + ConfirmForMs || Reported >= MaxReportsPerProcess)
            {
                _blockDone = true;
                return;
            }
            if (!_probe.IsNotResponding(out string message))
                return;
            _blockDone = true;
            Reported++;
            _onAnr(message ?? "", _probe.MainThreadStack(64) ?? "");
        }
    }
}
