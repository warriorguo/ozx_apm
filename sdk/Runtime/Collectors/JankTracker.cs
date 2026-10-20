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
        /// <summary>The expected frame time (baseline) when it began.</summary>
        public float ExpectedFrameMs;
        /// <summary>Ended because it reached MaxEpisodeMs (a lasting slowdown), not by normal frames.</summary>
        public bool Capped;
    }

    /// <summary>
    /// The jank definition (APM-21), without Unity: fed one frame time per
    /// frame, it says when a jank ended. Main thread only; no allocation after
    /// construction.
    ///
    /// - Expected frame time E: the median of the first 15 frames (warm-up),
    ///   then an exponential average of the normal frames (weight 1/16, about
    ///   the last 31 frames), at least the target frame time when the game set one (the
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
        /// <summary>Weight of a normal frame in the baseline (an exponential average over ~31 frames).</summary>
        internal const float BaselineAlpha = 1f / 16f;
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

        // Baseline: the median of the WarmupFrames first frames, then an
        // exponential average of the normal frames (O(1) per frame: no array
        // is touched once warmed up; APM-21 device check).
        private readonly float[] _warm = new float[WarmupFrames];
        private int _warmCount;
        private bool _hasBase;
        private float _base;
        private float _targetFrameMs;

        /// <summary>
        /// The slow threshold while the tracker is idle and watching (not in a
        /// jank, a grace period, warm-up or suspended); 0 otherwise. A frame in
        /// (0, FastLimitMs] is a normal frame: the fast path.
        /// </summary>
        private float _fastLimitMs;

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

        /// <summary>
        /// True for the frame that began a run of slow frames (the first frame
        /// of a possible jank): the caller notes the wall time and the context
        /// then, used if the run becomes a jank.
        /// </summary>
        internal bool RunStartedThisFrame { get; private set; }


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
        internal float TargetFrameMs
        {
            get => _targetFrameMs;
            set
            {
                _targetFrameMs = value;
                RefreshFast();
            }
        }

        /// <summary>The expected frame time (ms): the baseline, at least TargetFrameMs; 0 while warming up.</summary>
        internal float ExpectedFrameMs => _hasBase ? (_base > _targetFrameMs ? _base : _targetFrameMs) : 0f;

        /// <summary>The slow-frame threshold for the current baseline (ms); 0 while warming up.</summary>
        internal float SlowThresholdMs => SlowThreshold(ExpectedFrameMs);

        internal bool WarmingUp => !_hasBase;
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
            RefreshFast();
        }

        /// <summary>
        /// Starts a new baseline (WarmupFrames frames taken in without
        /// detection), e.g. when the target frame rate changed. A jank in
        /// progress must have been flushed before.
        /// </summary>
        internal void ResetBaseline()
        {
            _warmCount = 0;
            _hasBase = false;
            if (_state == State.Run)
                _state = State.Idle;
            RefreshFast();
        }

        /// <summary>Paused or unfocused: frames are ignored until Resume. Flush first.</summary>
        internal void Suspend()
        {
            _suspended = true;
            _state = State.Idle;
            RefreshFast();
        }

        /// <summary>Back in the foreground: the grace period, the baseline kept.</summary>
        internal void Resume()
        {
            _suspended = false;
            _state = State.Idle;
            StartGrace(GraceFrames, GraceMs);
            RefreshFast();
        }

        /// <summary>A scene was loaded in this frame: it and the next are not janks. Flush first.</summary>
        internal void SkipSceneLoad()
        {
            _state = State.Idle;
            StartGrace(SceneLoadFrames, 0f);
            RefreshFast();
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
            StartedThisFrame = false;
            RunStartedThisFrame = false;
            if (TryFast(frameMs))
            {
                episode = default;
                return false;
            }
            bool ended = FrameSlow(frameMs, out episode);
            RefreshFast();
            return ended;
        }

        /// <summary>
        /// The fast path, almost every frame: idle and a normal frame. A few
        /// fields of this object, no array, no call, no engine. Returns false
        /// (nothing done) for any other frame: then Frame must be called.
        /// (NaN fails the test.)
        /// </summary>
        internal bool TryFast(float frameMs)
        {
            if (!(frameMs > 0f && frameMs <= _fastLimitMs))
                return false;
            // StartedThisFrame / RunStartedThisFrame are not reset here: callers
            // read them only after Frame, which resets them.
            ClockMs += frameMs;
            float b = _base + (frameMs - _base) * BaselineAlpha;
            _base = b;
            float expected = b > _targetFrameMs ? b : _targetFrameMs;
            float slow = expected * SlowFactor;
            float margin = expected + SlowMarginMs;
            _fastLimitMs = slow > margin ? slow : margin;
            return true;
        }

        private void RefreshFast()
        {
            _fastLimitMs = _state == State.Idle && !_suspended && _hasBase && _graceFrames <= 0 && !(_graceMsLeft > 0f)
                ? SlowThreshold(ExpectedFrameMs)
                : 0f;
        }

        private bool FrameSlow(float frameMs, out JankEpisode episode)
        {
            episode = default;
            // NaN fails the comparison; 0 happens in the very first frame.
            if (!(frameMs > 0f) || float.IsInfinity(frameMs))
                return false;
            ClockMs += frameMs;
            if (_suspended)
                return false;
            if (_graceFrames > 0 || _graceMsLeft > 0f)
            {
                if (!_graceFirst)
                    _graceMsLeft -= frameMs;
                _graceFirst = false;
                if (_graceFrames > 0)
                    _graceFrames--;
                return false;
            }
            if (!_hasBase)
            {
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
                    RunStartedThisFrame = true;
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
                RefreshFast();
                return true;
            }
            _state = State.Idle;
            RefreshFast();
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
            if (_hasBase)
            {
                _base += (frameMs - _base) * BaselineAlpha;
                return;
            }
            // Warm-up: every frame, then the median of them (robust to the
            // long frames of a start or a change).
            int i = _warmCount++;
            while (i > 0 && _warm[i - 1] > frameMs)
            {
                _warm[i] = _warm[i - 1];
                i--;
            }
            _warm[i] = frameMs;
            if (_warmCount == WarmupFrames)
            {
                _base = _warm[WarmupFrames / 2];
                _hasBase = true;
            }
        }
    }

    /// <summary>
    /// At most MaxPerMinute janks sent in any 60 s of the detector's clock (a
    /// sliding window: the times of the last MaxPerMinute janks sent); the
    /// others are counted, not sent. Called once per jank, not per frame.
    /// </summary>
    internal sealed class JankBudget
    {
        internal const double WindowMs = 60000;
        private double[] _sent = new double[0];
        private int _count;
        private int _oldest;
        private double _last = double.NegativeInfinity;

        internal bool TryTake(double nowMs, int maxPerMinute)
        {
            if (maxPerMinute <= 0)
                return false;
            if (_sent.Length != maxPerMinute || nowMs < _last)
            {
                // A new limit, or a new clock (the detector restarted): start over.
                _sent = new double[maxPerMinute];
                _count = 0;
                _oldest = 0;
            }
            _last = nowMs;
            if (_count < _sent.Length)
            {
                _sent[(_oldest + _count) % _sent.Length] = nowMs;
                _count++;
                return true;
            }
            if (nowMs - _sent[_oldest] < WindowMs)
                return false;
            _sent[_oldest] = nowMs;
            _oldest = (_oldest + 1) % _sent.Length;
            return true;
        }
    }
}
