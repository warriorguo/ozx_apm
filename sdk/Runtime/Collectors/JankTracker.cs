using System;

namespace OzxApm.Collectors
{
    /// <summary>One jank as <see cref="JankTracker"/> found it (APM-21).</summary>
    internal struct JankEpisode
    {
        /// <summary>Tracker clock (ms) when the first slow frame began.</summary>
        public double StartMs;
        /// <summary>From the start of the first slow frame to the end of the last one (the normal frames of a merge gap in between included).</summary>
        public float DurationMs;
        /// <summary>The longest frame.</summary>
        public float MaxFrameMs;
        /// <summary>Frames over the slow threshold.</summary>
        public int SlowFrames;
        /// <summary>Every frame of the span (the slow ones and the normal ones of merge gaps).</summary>
        public int Frames;
        /// <summary>The expected frame time (baseline median) when it began.</summary>
        public float ExpectedFrameMs;
        /// <summary>Ended because it reached MaxEpisodeMs (a lasting slowdown), not by normal frames.</summary>
        public bool Capped;
    }

    /// <summary>
    /// The jank definition (APM-21), without Unity: fed one frame time per
    /// frame, it says when a jank ended. Main thread only; no allocation after
    /// construction.
    ///
    /// - Expected frame time E: the median of the last BaselineFrames normal
    ///   frames, at least the target frame time when the game set one (the
    ///   frame rate it actually runs at, not only the one it asks for: see
    ///   RELEASE.md APM-21).
    /// - A frame is slow when it is longer than max(1.5 E, E + 8 ms).
    /// - A jank begins with a run of consecutive slow frames lasting more than
    ///   JankThresholdMs (50 ms) in total: one slow frame over 50 ms, or e.g.
    ///   two 30 ms frames at 60 fps. Its first frame is part of it.
    /// - It ends after MergeGapMs of normal frames (slow frames within that gap
    ///   continue it), or when it has lasted MaxEpisodeMs (a lasting
    ///   slowdown: reported once, then the baseline starts again from the
    ///   new frame rate).
    /// - Slow frames never enter the baseline; frames of a grace period
    ///   (start, resume, scene load) are neither detected nor taken in.
    /// </summary>
    internal sealed class JankTracker
    {
        internal const float SlowFactor = 1.5f;
        internal const float SlowMarginMs = 8f;
        internal const int BaselineFrames = 31;
        internal const int WarmupFrames = 15;
        internal const float MergeGapMs = 250f;
        internal const float MaxEpisodeMs = 5000f;
        /// <summary>After start and resume: the first GraceFrames frames and GraceMs after the first one are ignored.</summary>
        internal const int GraceFrames = 2;
        internal const float GraceMs = 500f;
        /// <summary>A scene load: the frame of sceneLoaded and the next one are ignored.</summary>
        internal const int SceneLoadFrames = 2;

        private enum State { Idle, Run, Episode, Gap }

        private readonly float _jankMs;

        // Baseline: the last BaselineFrames normal frames, as a ring and sorted.
        private readonly float[] _ring = new float[BaselineFrames];
        private readonly float[] _sorted = new float[BaselineFrames];
        private int _count;
        private int _head;
        private int _warmup;

        private int _graceFrames;
        private float _graceMsLeft;
        private bool _graceFirst;
        private bool _suspended;

        private State _state;
        private double _start;
        private float _duration;
        private float _max;
        private int _slow;
        private int _frames;
        private float _expectedAtStart;
        private float _gapMs;
        private int _gapFrames;

        /// <summary>Sum of every frame time fed (ms): the tracker's clock.</summary>
        internal double ClockMs { get; private set; }

        /// <summary>True for the frame in which a jank began (the caller captures the context then).</summary>
        internal bool StartedThisFrame { get; private set; }

        /// <summary>Frames fed and frames ignored (grace, suspended, invalid), for tests and stats.</summary>
        internal long FramesSeen { get; private set; }
        internal long FramesIgnored { get; private set; }

        internal JankTracker(float jankThresholdMs)
        {
            _jankMs = jankThresholdMs > 0f && !float.IsInfinity(jankThresholdMs) ? jankThresholdMs : 50f;
            Restart();
        }

        /// <summary>
        /// The frame time the game asks for (1000 / Application.targetFrameRate;
        /// 0 = not set): the expected frame time is never below it, so a frame
        /// pacing that alternates display refreshes around the target (60 fps
        /// on a 90 Hz display: 11.1 and 22.2 ms) is not slow.
        /// </summary>
        internal float TargetFrameMs { get; set; }

        /// <summary>The expected frame time (ms): the baseline median, at least TargetFrameMs; 0 until the baseline has its first frames.</summary>
        internal float ExpectedFrameMs => _count == 0 ? 0f : Math.Max(_sorted[_count / 2], TargetFrameMs);

        /// <summary>The slow-frame threshold for the current baseline (ms); 0 while warming up.</summary>
        internal float SlowThresholdMs => SlowThreshold(ExpectedFrameMs);

        internal bool WarmingUp => _warmup > 0;
        internal bool InJank => _state == State.Episode || _state == State.Gap;

        internal static float SlowThreshold(float expectedMs)
        {
            if (expectedMs <= 0f)
                return 0f;
            return Math.Max(expectedMs * SlowFactor, expectedMs + SlowMarginMs);
        }

        /// <summary>Forgets everything: a new baseline, the start grace period.</summary>
        internal void Restart()
        {
            _state = State.Idle;
            _suspended = false;
            _graceFrames = 0;
            _graceMsLeft = 0f;
            ResetBaseline();
            StartGrace(GraceFrames, GraceMs);
        }

        /// <summary>
        /// Starts a new baseline (WarmupFrames frames taken in without
        /// detection), e.g. when the target frame rate changed. A jank in
        /// progress must have been flushed before.
        /// </summary>
        internal void ResetBaseline()
        {
            _count = 0;
            _head = 0;
            _warmup = WarmupFrames;
            if (_state == State.Run)
                _state = State.Idle;
        }

        /// <summary>Paused or unfocused: frames are ignored until Resume. Flush first.</summary>
        internal void Suspend()
        {
            _suspended = true;
            _state = State.Idle;
        }

        /// <summary>Back in the foreground: the grace period, the baseline kept.</summary>
        internal void Resume()
        {
            _suspended = false;
            _state = State.Idle;
            StartGrace(GraceFrames, GraceMs);
        }

        /// <summary>A scene was loaded in this frame: it and the next are not janks. Flush first.</summary>
        internal void SkipSceneLoad()
        {
            _state = State.Idle;
            StartGrace(SceneLoadFrames, 0f);
        }

        private void StartGrace(int frames, float ms)
        {
            if (frames > _graceFrames)
                _graceFrames = frames;
            if (ms > _graceMsLeft)
                _graceMsLeft = ms;
            // The first frame after a resume holds the time in the background:
            // the grace time counts from its end.
            _graceFirst = true;
        }

        /// <summary>
        /// One frame of frameMs. Returns true when a jank ended with this frame
        /// (episode is then set). No allocation.
        /// </summary>
        internal bool Frame(float frameMs, out JankEpisode episode)
        {
            episode = default;
            StartedThisFrame = false;
            FramesSeen++;
            // NaN fails the comparison; 0 happens in the very first frame.
            if (!(frameMs > 0f) || float.IsInfinity(frameMs))
            {
                FramesIgnored++;
                return false;
            }
            ClockMs += frameMs;
            if (_suspended)
            {
                FramesIgnored++;
                return false;
            }
            if (_graceFrames > 0 || _graceMsLeft > 0f)
            {
                if (!_graceFirst)
                    _graceMsLeft -= frameMs;
                _graceFirst = false;
                if (_graceFrames > 0)
                    _graceFrames--;
                FramesIgnored++;
                return false;
            }
            if (_warmup > 0)
            {
                _warmup--;
                AddBaseline(frameMs);
                return false;
            }

            float expected = ExpectedFrameMs;
            bool slow = frameMs > SlowThreshold(expected);
            switch (_state)
            {
                case State.Idle:
                    if (!slow)
                    {
                        AddBaseline(frameMs);
                        return false;
                    }
                    _start = ClockMs - frameMs;
                    _duration = frameMs;
                    _max = frameMs;
                    _slow = 1;
                    _frames = 1;
                    _expectedAtStart = expected;
                    _state = State.Run;
                    return Qualify(out episode);

                case State.Run:
                    if (!slow)
                    {
                        // Too short to be a jank: forgotten.
                        _state = State.Idle;
                        AddBaseline(frameMs);
                        return false;
                    }
                    AddSlow(frameMs);
                    return Qualify(out episode);

                case State.Episode:
                    if (slow)
                    {
                        AddSlow(frameMs);
                        return CheckCap(out episode);
                    }
                    _state = State.Gap;
                    _gapMs = frameMs;
                    _gapFrames = 1;
                    AddBaseline(frameMs);
                    return CheckGap(out episode);

                default: // Gap
                    if (slow)
                    {
                        // Within MergeGapMs: the same jank goes on.
                        _duration += _gapMs;
                        _frames += _gapFrames;
                        _state = State.Episode;
                        AddSlow(frameMs);
                        return CheckCap(out episode);
                    }
                    _gapMs += frameMs;
                    _gapFrames++;
                    AddBaseline(frameMs);
                    return CheckGap(out episode);
            }
        }

        /// <summary>
        /// Ends a jank in progress now (pause, scene load, target frame rate
        /// change, stop). A run of slow frames too short to be a jank is
        /// forgotten. Returns true when there was a jank.
        /// </summary>
        internal bool Flush(out JankEpisode episode)
        {
            episode = default;
            if (_state == State.Episode || _state == State.Gap)
            {
                Complete(false, out episode);
                return true;
            }
            _state = State.Idle;
            return false;
        }

        private void AddSlow(float frameMs)
        {
            _duration += frameMs;
            _slow++;
            _frames++;
            if (frameMs > _max)
                _max = frameMs;
        }

        private bool Qualify(out JankEpisode episode)
        {
            episode = default;
            if (_duration > _jankMs)
            {
                _state = State.Episode;
                StartedThisFrame = true;
                return CheckCap(out episode);
            }
            return false;
        }

        private bool CheckCap(out JankEpisode episode)
        {
            episode = default;
            if (_duration < MaxEpisodeMs)
                return false;
            bool sustained = _slow > 1;
            Complete(true, out episode);
            // A lasting slowdown: from now on its frame rate is the norm (a
            // single very long frame says nothing about the frame rate).
            if (sustained)
                ResetBaseline();
            return true;
        }

        private bool CheckGap(out JankEpisode episode)
        {
            episode = default;
            if (_gapMs < MergeGapMs)
                return false;
            Complete(false, out episode);
            return true;
        }

        private void Complete(bool capped, out JankEpisode episode)
        {
            episode = new JankEpisode
            {
                StartMs = _start,
                DurationMs = _duration,
                MaxFrameMs = _max,
                SlowFrames = _slow,
                Frames = _frames,
                ExpectedFrameMs = _expectedAtStart,
                Capped = capped,
            };
            _state = State.Idle;
        }

        private void AddBaseline(float frameMs)
        {
            if (_count < BaselineFrames)
            {
                _ring[(_head + _count) % BaselineFrames] = frameMs;
                Insert(frameMs, _count);
                _count++;
                return;
            }
            float old = _ring[_head];
            _ring[_head] = frameMs;
            _head = (_head + 1) % BaselineFrames;
            // Remove old from the sorted array, then insert the new value.
            int i = Array.BinarySearch<float>(_sorted, 0, _count, old);
            if (i < 0)
                i = ~i; // cannot happen; keeps the array consistent anyway
            if (i < _count - 1)
                Array.Copy(_sorted, i + 1, _sorted, i, _count - 1 - i);
            Insert(frameMs, _count - 1);
        }

        // Inserts value into _sorted[0..n) (n < capacity) keeping it sorted.
        private void Insert(float value, int n)
        {
            int i = n;
            while (i > 0 && _sorted[i - 1] > value)
            {
                _sorted[i] = _sorted[i - 1];
                i--;
            }
            _sorted[i] = value;
        }
    }

    /// <summary>
    /// At most MaxPerMinute janks sent per minute of the tracker's clock (a
    /// fixed window); the others are counted, not sent.
    /// </summary>
    internal sealed class JankBudget
    {
        internal const double WindowMs = 60000;
        private double _windowStart = double.NegativeInfinity;
        private int _used;

        internal bool TryTake(double nowMs, int maxPerMinute)
        {
            if (nowMs - _windowStart >= WindowMs || nowMs < _windowStart)
            {
                _windowStart = nowMs;
                _used = 0;
            }
            if (_used >= maxPerMinute)
                return false;
            _used++;
            return true;
        }
    }
}
