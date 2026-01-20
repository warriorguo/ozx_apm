using System;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Tracks app startup timing across multiple phases
    /// </summary>
    public class StartupTracker : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;
        private bool _hasReported;

        // Timing markers
        private float _appLaunchTime;      // When the native app started (estimated)
        private float _unityInitTime;       // When Unity initialized
        private float _firstFrameTime;      // When first frame rendered
        private float _ttiTime;             // When Time To Interactive was marked

        private bool _firstFrameRecorded;
        private int _frameCount;

        public bool IsActive => _isActive;

        public StartupTracker(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _hasReported = false;
            _firstFrameRecorded = false;
            _frameCount = 0;

            // Estimate times based on what we know
            // In a real implementation, native plugins would provide exact timestamps
            _unityInitTime = Time.realtimeSinceStartup;

            // Estimate app launch time (Unity init is typically 100-500ms after app launch on mobile)
            _appLaunchTime = 0; // We don't have exact native time, treat Unity init as reference
        }

        public void Update()
        {
            if (!_isActive || _hasReported)
                return;

            _frameCount++;

            // Record first frame time
            if (!_firstFrameRecorded && _frameCount > 1)
            {
                _firstFrameTime = Time.realtimeSinceStartup;
                _firstFrameRecorded = true;
            }
        }

        public void Stop()
        {
            _isActive = false;
        }

        /// <summary>
        /// Call this when the app is ready for user interaction
        /// </summary>
        public void MarkTTI()
        {
            if (_hasReported)
                return;

            _ttiTime = Time.realtimeSinceStartup;
            ReportStartup();
        }

        /// <summary>
        /// Manually set phase 1 timing (native app to Unity init)
        /// Call this from native plugin if available
        /// </summary>
        public void SetPhase1Time(float milliseconds)
        {
            _appLaunchTime = _unityInitTime - (milliseconds / 1000f);
        }

        private void ReportStartup()
        {
            if (_hasReported)
                return;

            // Calculate phase timings
            float phase1Ms = (_unityInitTime - _appLaunchTime) * 1000f;
            float phase2Ms = (_firstFrameTime - _unityInitTime) * 1000f;
            float ttiMs = (_ttiTime - _firstFrameTime) * 1000f;

            // Sanity checks
            if (phase1Ms < 0) phase1Ms = 0;
            if (phase2Ms < 0) phase2Ms = 0;
            if (ttiMs < 0) ttiMs = 0;

            var evt = new StartupEvent
            {
                phase1_ms = phase1Ms,
                phase2_ms = phase2Ms,
                tti_ms = ttiMs
            };

            _client.EnqueueEvent(evt);
            _hasReported = true;

            float totalMs = phase1Ms + phase2Ms + ttiMs;
            ApmClient.Log(LogLevel.Info, $"Startup tracked: total={totalMs:F0}ms (P1={phase1Ms:F0}, P2={phase2Ms:F0}, TTI={ttiMs:F0})");
        }

        /// <summary>
        /// Gets whether startup has been reported
        /// </summary>
        public bool HasReported => _hasReported;
    }
}
