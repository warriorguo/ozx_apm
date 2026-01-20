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

        // Recent events for context (circular buffer)
        private readonly Queue<string> _recentEvents = new Queue<string>();
        private const int MaxRecentEvents = 10;

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
        /// Records an event for jank context
        /// </summary>
        public void RecordEvent(string eventDescription)
        {
            if (_recentEvents.Count >= MaxRecentEvents)
            {
                _recentEvents.Dequeue();
            }
            _recentEvents.Enqueue($"{Time.realtimeSinceStartup:F2}:{eventDescription}");
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
                recent_events = new List<string>(_recentEvents)
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
