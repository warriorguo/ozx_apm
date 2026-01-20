using System;
using UnityEngine;
using UnityEngine.Profiling;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Tracks memory usage and GC activity
    /// </summary>
    public class MemoryCollector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;
        private int _gcCount;
        private float _gcAllocKb;
        private long _lastGcMemory;
        private int _lastGcCollectionCount;
        private float _lastResetTime;
        private const float ResetIntervalSeconds = 10f;

        public bool IsActive => _isActive;

        public MemoryCollector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _lastGcMemory = GC.GetTotalMemory(false);
            _lastGcCollectionCount = GC.CollectionCount(0);
            _lastResetTime = Time.realtimeSinceStartup;
            _gcCount = 0;
            _gcAllocKb = 0;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            // Track GC collections
            int currentGcCount = GC.CollectionCount(0);
            if (currentGcCount > _lastGcCollectionCount)
            {
                _gcCount += currentGcCount - _lastGcCollectionCount;
                _lastGcCollectionCount = currentGcCount;
            }

            // Track allocations (approximate)
            long currentMemory = GC.GetTotalMemory(false);
            if (currentMemory > _lastGcMemory)
            {
                _gcAllocKb += (currentMemory - _lastGcMemory) / 1024f;
            }
            _lastGcMemory = currentMemory;

            // Reset counters periodically
            if (Time.realtimeSinceStartup - _lastResetTime > ResetIntervalSeconds)
            {
                ResetCounters();
            }
        }

        public void Stop()
        {
            _isActive = false;
        }

        /// <summary>
        /// Gets recent GC stats for other collectors
        /// </summary>
        public (int gcCount, float gcAllocKb) GetRecentStats()
        {
            return (_gcCount, _gcAllocKb);
        }

        /// <summary>
        /// Gets current memory usage in MB
        /// </summary>
        public float GetUsedMemoryMB()
        {
            return GC.GetTotalMemory(false) / (1024f * 1024f);
        }

        /// <summary>
        /// Gets total reserved memory in MB (if available)
        /// </summary>
        public float GetReservedMemoryMB()
        {
            try
            {
                return Profiler.GetTotalReservedMemoryLong() / (1024f * 1024f);
            }
            catch
            {
                return GetUsedMemoryMB();
            }
        }

        /// <summary>
        /// Gets mono heap size in MB
        /// </summary>
        public float GetMonoHeapSizeMB()
        {
            try
            {
                return Profiler.GetMonoHeapSizeLong() / (1024f * 1024f);
            }
            catch
            {
                return 0;
            }
        }

        private void ResetCounters()
        {
            _gcCount = 0;
            _gcAllocKb = 0;
            _lastResetTime = Time.realtimeSinceStartup;
        }
    }
}
