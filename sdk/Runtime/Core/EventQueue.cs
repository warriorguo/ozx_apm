using System;
using System.Collections.Generic;
using UnityEngine;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>
    /// Manages event batching and queuing for efficient transmission
    /// </summary>
    public class EventQueue
    {
        private readonly RingBuffer<BaseEvent> _buffer;
        private readonly ApmConfig _config;
        private readonly List<BaseEvent> _batchBuffer;
        private readonly object _lock = new object();

        private float _lastFlushTime;
        private int _currentBatchSize;

        public event Action<List<BaseEvent>> OnBatchReady;

        public int Count => _buffer.Count;
        public bool IsFull => _buffer.IsFull;

        public EventQueue(ApmConfig config)
        {
            _config = config;
            _buffer = new RingBuffer<BaseEvent>(config.MaxQueueSize);
            _batchBuffer = new List<BaseEvent>(config.BatchSize);
            _lastFlushTime = Time.realtimeSinceStartup;
            _currentBatchSize = 0;
        }

        /// <summary>
        /// Enqueues an event for batched transmission
        /// </summary>
        public void Enqueue(BaseEvent evt)
        {
            if (evt == null)
                return;

            lock (_lock)
            {
                _buffer.EnqueueOverwrite(evt);
                _currentBatchSize++;

                // Check if we should flush
                if (ShouldFlush())
                {
                    FlushInternal();
                }
            }
        }

        /// <summary>
        /// Updates the queue, checking for time-based flush
        /// </summary>
        public void Update()
        {
            lock (_lock)
            {
                if (ShouldFlush())
                {
                    FlushInternal();
                }
            }
        }

        /// <summary>
        /// Forces a flush of all queued events
        /// </summary>
        public void Flush()
        {
            lock (_lock)
            {
                FlushInternal();
            }
        }

        /// <summary>
        /// Clears all queued events
        /// </summary>
        public void Clear()
        {
            lock (_lock)
            {
                _buffer.Clear();
                _batchBuffer.Clear();
                _currentBatchSize = 0;
            }
        }

        private bool ShouldFlush()
        {
            // Flush if batch size reached
            if (_currentBatchSize >= _config.BatchSize)
                return true;

            // Flush if interval elapsed and we have events
            float elapsed = Time.realtimeSinceStartup - _lastFlushTime;
            if (elapsed >= _config.FlushIntervalSeconds && _currentBatchSize > 0)
                return true;

            return false;
        }

        private void FlushInternal()
        {
            if (_buffer.IsEmpty)
            {
                _lastFlushTime = Time.realtimeSinceStartup;
                _currentBatchSize = 0;
                return;
            }

            _batchBuffer.Clear();

            // Dequeue up to batch size
            int maxDequeue = Math.Min(_config.BatchSize, _buffer.Count);
            for (int i = 0; i < maxDequeue; i++)
            {
                if (_buffer.TryDequeue(out BaseEvent evt))
                {
                    _batchBuffer.Add(evt);
                }
                else
                {
                    break;
                }
            }

            _lastFlushTime = Time.realtimeSinceStartup;
            _currentBatchSize = 0;

            if (_batchBuffer.Count > 0)
            {
                // Create a copy of the list to pass to the callback
                var batch = new List<BaseEvent>(_batchBuffer);
                OnBatchReady?.Invoke(batch);
            }
        }

        /// <summary>
        /// Gets approximate memory usage in bytes
        /// </summary>
        public int GetMemoryUsageBytes()
        {
            // Rough estimate: 400 bytes per event average
            return _buffer.Count * 400;
        }
    }
}
