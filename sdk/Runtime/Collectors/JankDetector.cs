using System;
using System.Collections.Generic;
using System.Threading;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Detects janks (APM-21): frames much longer than the frame rate the game
    /// was running at. The definition is JankTracker's; this class feeds it
    /// Time.unscaledDeltaTime once per frame, suspends it while the app is
    /// paused or unfocused, skips scene-load frames, starts a new baseline when
    /// the target frame rate changes, and sends what it finds (at most
    /// ApmConfig.MaxJanksPerMinute a minute). Per frame: two engine property
    /// reads and the tracker, no allocation.
    /// </summary>
    public class JankDetector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;
        private JankTracker _tracker;
        private readonly JankBudget _budget = new JankBudget();

        private bool _isActive;
        private bool _paused;
        private bool _unfocused;
        private bool _suspended;
        private int _targetFrameRate;
        private int _vSyncCount;
        // The game context when the jank in progress began (APM-5): a room
        // switch's hitch is reported with the room it happened in.
        private ContextSnapshot _startContext;
        // When the jank in progress began (Unix ms), noted in the frame of its
        // first slow frame (not when it is sent: a flush at a scene load comes
        // after the load blocked, before that frame's time is known).
        private long _startWallMs;
        private ContextSnapshot _runStartContext;
        private long _runStartWallMs;

        // Counters, read by ApmClient.GetStats (main thread writes, any thread reads).
        internal long Detected;
        internal long Sent;
        internal long Dropped;
        internal float LastDurationMs;
        internal float LastMaxFrameMs;
        internal int LastFrames;
        internal int LastSlowFrames;

        // What happened just before (APM-6): the time line's breadcrumbs
        // (custom events, scene loads, exceptions) and warning/error lines of
        // the RecentWindowMs before the jank started, at most these many.
        internal const int MaxRecentBreadcrumbs = 10;
        internal const int MaxRecentLogLines = 5;
        internal const int RecentWindowMs = 10000;
        internal const int MaxRecentLength = 200;

        public bool IsActive => _isActive;

        /// <summary>The expected frame time the detector currently compares with (ms; 0 while warming up).</summary>
        internal float ExpectedFrameMs => _tracker != null && !_tracker.WarmingUp ? _tracker.ExpectedFrameMs : 0f;

        internal JankTracker TrackerForTests => _tracker;
        /// <summary>Without a client (tests): the time line janks read, and where they go.</summary>
        internal Timeline TimelineForTests;
        internal List<JankEvent> SentForTests;

        public JankDetector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            StartWith(Application.targetFrameRate, QualitySettings.vSyncCount);
        }

        internal void StartWith(int targetFrameRate, int vSyncCount)
        {
            _tracker = new JankTracker(_config.JankThresholdMs);
            _targetFrameRate = targetFrameRate;
            _vSyncCount = vSyncCount;
            _tracker.TargetFrameMs = TargetFrameMs(targetFrameRate);
            _startContext = null;
            _suspended = _paused || _unfocused;
            if (_suspended)
                _tracker.Suspend();
            _isActive = true;
        }

        /// <summary>
        /// The frame time a targetFrameRate asks for; 0 when it is not set (-1:
        /// the platform's default, 30 fps on mobile, or vSyncCount decides) or
        /// above 1000 fps.
        /// </summary>
        internal static float TargetFrameMs(int targetFrameRate)
        {
            return targetFrameRate > 0 && targetFrameRate <= 1000 ? 1000f / targetFrameRate : 0f;
        }

        public void Update()
        {
            if (!_isActive)
                return;
            // Unscaled: Time.timeScale (slow motion, a pause menu at 0) does not change it.
            UpdateWith(Time.unscaledDeltaTime * 1000f, Application.targetFrameRate, QualitySettings.vSyncCount);
        }

        /// <summary>One frame of frameMs with the engine's current target frame rate and vSyncCount.</summary>
        internal void UpdateWith(float frameMs, int target, int vSync)
        {
            if (!_isActive)
                return;

            // A new target frame rate (e.g. a 60 fps setting) is a new rhythm,
            // not a jank: a jank in progress ends, the baseline starts again.
            if (target != _targetFrameRate || vSync != _vSyncCount)
            {
                _targetFrameRate = target;
                _vSyncCount = vSync;
                FlushJank();
                _tracker.ResetBaseline();
                _tracker.TargetFrameMs = TargetFrameMs(target);
            }

            bool ended = _tracker.Frame(frameMs, out JankEpisode episode);
            if (_tracker.RunStartedThisFrame)
            {
                // The first slow frame just ended: the possible jank began
                // frameMs ago, in this context. Only slow frames get here.
                _runStartContext = GameContext.Current;
                _runStartWallMs = Timeline.NowMs() - (long)frameMs;
            }
            if (_tracker.StartedThisFrame)
            {
                _startContext = _runStartContext;
                _startWallMs = _runStartWallMs;
            }
            if (ended)
                ReportJank(episode);
        }

        public void Stop()
        {
            if (_isActive)
                FlushJank();
            _isActive = false;
        }

        /// <summary>OnApplicationPause: a jank in progress is sent; no frame is looked at until resumed.</summary>
        internal void OnPause(bool paused)
        {
            _paused = paused;
            UpdateSuspended();
        }

        /// <summary>OnApplicationFocus: like a pause (the Editor and some platforms stop the player loop without focus).</summary>
        internal void OnFocus(bool hasFocus)
        {
            _unfocused = !hasFocus;
            UpdateSuspended();
        }

        /// <summary>
        /// SceneManager.sceneLoaded, before the client takes the new scene's
        /// name: a jank in progress is sent with the scene it happened in; the
        /// loading frame and the next are not janks (scene_load measures them).
        /// </summary>
        internal void OnSceneLoaded()
        {
            if (!_isActive)
                return;
            FlushJank();
            _tracker.SkipSceneLoad();
        }

        private void UpdateSuspended()
        {
            if (!_isActive)
                return;
            bool suspend = _paused || _unfocused;
            if (suspend == _suspended)
                return;
            _suspended = suspend;
            if (suspend)
            {
                FlushJank();
                _tracker.Suspend();
            }
            else
            {
                _tracker.Resume();
            }
        }

        private void FlushJank()
        {
            if (_tracker != null && _tracker.Flush(out JankEpisode episode))
                ReportJank(episode);
        }

        /// <summary>
        /// Records a breadcrumb that later janks (and exceptions) carry. Same
        /// as ApmClient.RecordBreadcrumb.
        /// </summary>
        public void RecordEvent(string eventDescription)
        {
            _client?.Timeline?.RecordText(eventDescription);
        }

        /// <summary>The recent_events of a jank that began at startMs (Unix ms): the RecentWindowMs before it and what happened since.</summary>
        internal static List<string> RecentEvents(Timeline timeline, long startMs)
        {
            if (timeline == null)
                return new List<string>();
            return timeline.Snapshot(MaxRecentBreadcrumbs, MaxRecentLogLines, MaxRecentLength, MaxRecentLength, startMs - RecentWindowMs);
        }

        private void ReportJank(in JankEpisode episode)
        {
            Interlocked.Increment(ref Detected);
            LastDurationMs = episode.DurationMs;
            LastMaxFrameMs = episode.MaxFrameMs;
            LastFrames = episode.Frames;
            LastSlowFrames = episode.SlowFrames;
            var context = _startContext;
            long startMs = _startWallMs;
            _startContext = null;
            _runStartContext = null;

            // Without technical consent (e.g. the flush at Stop when it was
            // revoked) the event would be dropped at enqueue: not counted as sent.
            if (_client != null && !_config.TechnicalAllowed)
                return;
            if (!_budget.TryTake(_tracker.ClockMs, Math.Max(0, _config.MaxJanksPerMinute)))
            {
                Interlocked.Increment(ref Dropped);
                return;
            }

            var (gcCount, gcAllocKb) = _client != null ? _client.GetMemoryStats() : (0, 0f);

            var evt = new JankEvent
            {
                timestamp = startMs,
                duration_ms = episode.DurationMs,
                max_frame_ms = episode.MaxFrameMs,
                recent_gc_count = gcCount,
                recent_gc_alloc_kb = gcAllocKb,
                recent_events = RecentEvents(_client != null ? _client.Timeline : TimelineForTests, startMs),
                ctx = context,
            };

            if (_client != null)
                _client.EnqueueEvent(evt);
            else
                SentForTests?.Add(evt);
            Interlocked.Increment(ref Sent);

            if (ApmClient.IsLogEnabled(LogLevel.Debug))
                ApmClient.Log(LogLevel.Debug, $"Jank: {episode.DurationMs:F1} ms, max frame {episode.MaxFrameMs:F1} ms, {episode.SlowFrames}/{episode.Frames} frames slow, expected {episode.ExpectedFrameMs:F1} ms{(episode.Capped ? ", capped" : "")}");
        }
    }
}
