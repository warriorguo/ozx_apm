using System;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Collects FPS and frame time performance samples
    /// </summary>
    public class PerformanceCollector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private float _lastSampleTime;
        private int _frameCount;
        private float _frameTimeAccumulator;
        private float _maxFrameTime;
        private bool _isActive;

        public bool IsActive => _isActive;

        // The last sample's figures, read by the crash journal's thread (APM-7).
        // Single floats: a torn read is impossible, a stale one harmless.
        internal volatile float LastFps;
        internal volatile float LastFrameMs;
        internal volatile float LastMaxFrameMs;
        internal volatile float LastMemMb;
        internal volatile int LastGcCount;

        public PerformanceCollector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _lastSampleTime = Time.realtimeSinceStartup;
            _frameCount = 0;
            _frameTimeAccumulator = 0;
            _maxFrameTime = 0;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            float deltaTime = Time.unscaledDeltaTime;
            float frameTimeMs = deltaTime * 1000f;

            _frameCount++;
            _frameTimeAccumulator += frameTimeMs;
            if (frameTimeMs > _maxFrameTime)
                _maxFrameTime = frameTimeMs;

            float elapsed = Time.realtimeSinceStartup - _lastSampleTime;
            if (elapsed >= _config.SamplingIntervalSeconds)
            {
                EmitSample(elapsed);
            }
        }

        public void Stop()
        {
            _isActive = false;
        }

        private void EmitSample(float elapsed)
        {
            if (_frameCount == 0)
            {
                ResetCounters();
                return;
            }

            float avgFrameTime = _frameTimeAccumulator / _frameCount;
            float fps = _frameCount / elapsed;

            var (gcCount, gcAllocKb) = _client.GetMemoryStats();

            // Pooled: emitting a sample allocates nothing on the main thread. The
            // storage worker returns the event to the pool after writing it.
            var evt = PerfSamplePool.Rent();
            evt.fps = fps;
            evt.frame_time_ms = avgFrameTime;
            evt.main_thread_ms = avgFrameTime; // In Unity, main thread ≈ frame time for most cases
            evt.gc_alloc_kb = gcAllocKb;
            evt.mem_mb = _client.GetUsedMemoryMB();
            // APM-9: the play time the sample covers, and the memory breakdown
            // (a few engine calls, no allocation; once per sample).
            evt.interval_ms = (int)Math.Min(int.MaxValue, Math.Round(elapsed * 1000.0));
            _client.SampleMemory(evt);
            LastFps = fps;
            LastFrameMs = avgFrameTime;
            LastMaxFrameMs = _maxFrameTime;
            LastMemMb = evt.mem_mb;
            LastGcCount = gcCount;

            _client.EnqueueEvent(evt);
            ResetCounters();
        }

        private void ResetCounters()
        {
            _lastSampleTime = Time.realtimeSinceStartup;
            _frameCount = 0;
            _frameTimeAccumulator = 0;
            _maxFrameTime = 0;
        }
    }
}
