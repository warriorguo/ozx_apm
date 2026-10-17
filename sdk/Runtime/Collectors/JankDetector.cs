using System.Collections.Generic;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Detects jank (frame drops) based on configurable thresholds
    /// </summary>
    public class JankDetector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;
        private float _jankStartTime;
        private float _maxFrameTime;
        private bool _inJank;
        private int _consecutiveJankFrames;
        private float _lastJankReportTime;
        private const float JankReportCooldown = 1.0f; // Minimum seconds between jank reports

        // What happened just before (APM-6): the time line's breadcrumbs
        // (custom events, scene loads, exceptions) and warning/error lines of
        // the RecentWindowMs before the jank started, at most these many.
        internal const int MaxRecentBreadcrumbs = 10;
        internal const int MaxRecentLogLines = 5;
        internal const int RecentWindowMs = 10000;
        internal const int MaxRecentLength = 200;

        public bool IsActive => _isActive;

        public JankDetector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _inJank = false;
            _jankStartTime = 0;
            _maxFrameTime = 0;
            _consecutiveJankFrames = 0;
            _lastJankReportTime = -JankReportCooldown;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            float frameTimeMs = Time.unscaledDeltaTime * 1000f;

            // Single frame jank detection (> 50ms)
            if (frameTimeMs > _config.JankThresholdMs)
            {
                if (!_inJank)
                {
                    // Start of jank
                    _inJank = true;
                    _jankStartTime = Time.realtimeSinceStartup;
                    _maxFrameTime = frameTimeMs;
                    _consecutiveJankFrames = 1;
                }
                else
                {
                    // Continuing jank
                    _consecutiveJankFrames++;
                    if (frameTimeMs > _maxFrameTime)
                        _maxFrameTime = frameTimeMs;
                }
            }
            // Sustained jank detection (consecutive frames > 33ms)
            else if (frameTimeMs > _config.SustainedJankThresholdMs && _inJank)
            {
                _consecutiveJankFrames++;
                if (frameTimeMs > _maxFrameTime)
                    _maxFrameTime = frameTimeMs;
            }
            else if (_inJank)
            {
                // End of jank - report if significant
                float jankDuration = (Time.realtimeSinceStartup - _jankStartTime) * 1000f;
                if (jankDuration > _config.JankThresholdMs && CanReportJank())
                {
                    ReportJank(jankDuration);
                }
                ResetJankState();
            }
        }

        public void Stop()
        {
            _isActive = false;
        }

        /// <summary>
        /// Records a breadcrumb that later janks (and exceptions) carry. Same
        /// as ApmClient.RecordBreadcrumb.
        /// </summary>
        public void RecordEvent(string eventDescription)
        {
            _client?.Timeline?.RecordText(eventDescription);
        }

        /// <summary>The recent_events of a jank that lasted durationMs and ended at endMs.</summary>
        internal static List<string> RecentEvents(Timeline timeline, long endMs, float durationMs)
        {
            if (timeline == null)
                return new List<string>();
            long since = endMs - (long)durationMs - RecentWindowMs;
            return timeline.Snapshot(MaxRecentBreadcrumbs, MaxRecentLogLines, MaxRecentLength, MaxRecentLength, since);
        }

        private bool CanReportJank()
        {
            return Time.realtimeSinceStartup - _lastJankReportTime >= JankReportCooldown;
        }

        private void ReportJank(float durationMs)
        {
            var (gcCount, gcAllocKb) = _client.GetMemoryStats();

            var evt = new JankEvent
            {
                duration_ms = durationMs,
                max_frame_ms = _maxFrameTime,
                recent_gc_count = gcCount,
                recent_gc_alloc_kb = gcAllocKb,
                recent_events = RecentEvents(_client.Timeline, Timeline.NowMs(), durationMs)
            };

            _client.EnqueueEvent(evt);
            _lastJankReportTime = Time.realtimeSinceStartup;

            ApmClient.Log(LogLevel.Debug, $"Jank detected: {durationMs:F1}ms, max frame: {_maxFrameTime:F1}ms");
        }

        private void ResetJankState()
        {
            _inJank = false;
            _jankStartTime = 0;
            _maxFrameTime = 0;
            _consecutiveJankFrames = 0;
        }
    }
}
