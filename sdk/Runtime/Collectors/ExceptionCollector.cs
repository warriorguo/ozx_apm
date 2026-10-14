using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Captures unhandled exceptions and errors
    /// </summary>
    public class ExceptionCollector : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;

        // Deduplication
        private readonly Dictionary<string, ExceptionInfo> _recentExceptions = new Dictionary<string, ExceptionInfo>();
        private const int MaxRecentExceptions = 100;
        private const float DedupeWindowSeconds = 60f;
        private float _lastCleanupTime;

        // Breadcrumbs for crash context
        private readonly Queue<string> _breadcrumbs = new Queue<string>();
        private const int MaxBreadcrumbs = 50;

        // Within a dedupe window, the first occurrence of a fingerprint is enqueued
        // (and so persisted) immediately with count=1. Further occurrences are only
        // counted and enqueued as one event with count=N at the end of the window,
        // timestamped with the first repeat. sum(count) stays exact, and a crash
        // right after an exception does not lose it.
        private class ExceptionInfo
        {
            public string Fingerprint;
            public string Message;
            public string Stack;
            public int RepeatCount;
            public long RepeatFirstSeenMs;
        }

        public bool IsActive => _isActive;

        public ExceptionCollector(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;
            _lastCleanupTime = Time.realtimeSinceStartup;

            // Subscribe to Unity's log callback
            Application.logMessageReceived += OnLogMessage;
        }

        public void Update()
        {
            if (!_isActive)
                return;

            // Periodically flush deduplicated exceptions and cleanup
            float now = Time.realtimeSinceStartup;
            if (now - _lastCleanupTime > DedupeWindowSeconds)
            {
                FlushExceptions();
                _lastCleanupTime = now;
            }
        }

        public void Stop()
        {
            _isActive = false;
            Application.logMessageReceived -= OnLogMessage;

            // Final flush
            FlushExceptions();
        }

        /// <summary>
        /// Enqueues the repeat counts collected so far (e.g. before going to background).
        /// </summary>
        public void FlushPending()
        {
            FlushExceptions();
            _lastCleanupTime = Time.realtimeSinceStartup;
        }

        /// <summary>
        /// Records a breadcrumb for crash context
        /// </summary>
        public void RecordBreadcrumb(string message)
        {
            if (string.IsNullOrEmpty(message))
                return;

            if (_breadcrumbs.Count >= MaxBreadcrumbs)
            {
                _breadcrumbs.Dequeue();
            }

            string timestamp = DateTime.UtcNow.ToString("HH:mm:ss.fff", CultureInfo.InvariantCulture);
            _breadcrumbs.Enqueue($"[{timestamp}] {message}");
        }

        /// <summary>
        /// Gets current breadcrumbs for crash reports
        /// </summary>
        public List<string> GetBreadcrumbs()
        {
            return new List<string>(_breadcrumbs);
        }

        private void OnLogMessage(string message, string stackTrace, LogType type)
        {
            // An exception thrown from a log callback would be logged again and
            // land right back here: never let anything escape.
            try
            {
                HandleLogMessage(message, stackTrace, type);
            }
            catch
            {
                // Swallowed on purpose.
            }
        }

        private void HandleLogMessage(string message, string stackTrace, LogType type)
        {
            if (!_isActive)
                return;

            // Only capture errors and exceptions
            if (type != LogType.Error && type != LogType.Exception && type != LogType.Assert)
                return;

            // Never capture the SDK's own output.
            if (message != null && message.StartsWith(ApmClient.LogPrefix, StringComparison.Ordinal))
                return;

            message = message ?? "";
            string fingerprint = GenerateFingerprint(message, stackTrace);
            long nowMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();

            if (_recentExceptions.TryGetValue(fingerprint, out var existing))
            {
                // Deduplicate - count it, report at the end of the window
                if (existing.RepeatCount == 0)
                    existing.RepeatFirstSeenMs = nowMs;
                existing.RepeatCount++;
            }
            else
            {
                // New exception
                if (_recentExceptions.Count >= MaxRecentExceptions)
                {
                    FlushExceptions();
                }

                var info = new ExceptionInfo
                {
                    Fingerprint = fingerprint,
                    Message = TruncateMessage(message, 500),
                    Stack = TruncateStack(stackTrace, 4000)
                };
                _recentExceptions[fingerprint] = info;

                // First occurrence goes to disk right away, with the time it happened.
                Enqueue(info, 1, nowMs);
            }

            // Record as breadcrumb
            RecordBreadcrumb($"Exception: {TruncateMessage(message, 100)}");
        }

        private void FlushExceptions()
        {
            foreach (var kvp in _recentExceptions)
            {
                var info = kvp.Value;
                if (info.RepeatCount > 0)
                    Enqueue(info, info.RepeatCount, info.RepeatFirstSeenMs);
            }

            _recentExceptions.Clear();
        }

        private void Enqueue(ExceptionInfo info, int count, long timestampMs)
        {
            var evt = new ExceptionEvent
            {
                timestamp = timestampMs,
                fingerprint = info.Fingerprint,
                message = info.Message,
                stack = info.Stack,
                count = count
            };

            _client.EnqueueEvent(evt);
        }

        private string GenerateFingerprint(string message, string stackTrace)
        {
            // Create a fingerprint based on the first line of stack and message type
            string firstStackLine = "";
            if (!string.IsNullOrEmpty(stackTrace))
            {
                int newlineIndex = stackTrace.IndexOf('\n');
                firstStackLine = newlineIndex > 0 ? stackTrace.Substring(0, newlineIndex) : stackTrace;
            }

            // Remove line numbers for grouping
            firstStackLine = System.Text.RegularExpressions.Regex.Replace(firstStackLine, @":\d+", "");

            string input = $"{GetExceptionType(message)}|{firstStackLine}";

            using (var md5 = MD5.Create())
            {
                byte[] hash = md5.ComputeHash(Encoding.UTF8.GetBytes(input));
                return BitConverter.ToString(hash).Replace("-", "").ToLowerInvariant().Substring(0, 16);
            }
        }

        private string GetExceptionType(string message)
        {
            // Try to extract exception type from message
            int colonIndex = message.IndexOf(':');
            if (colonIndex > 0 && colonIndex < 100)
            {
                return message.Substring(0, colonIndex).Trim();
            }
            return "UnknownException";
        }

        private string TruncateMessage(string message, int maxLength)
        {
            if (string.IsNullOrEmpty(message))
                return "";
            if (message.Length <= maxLength)
                return message;
            return message.Substring(0, maxLength - 3) + "...";
        }

        private string TruncateStack(string stack, int maxLength)
        {
            if (string.IsNullOrEmpty(stack))
                return "";
            if (stack.Length <= maxLength)
                return stack;

            // Keep the beginning (most important) and note truncation
            return stack.Substring(0, maxLength - 20) + "\n[truncated...]";
        }
    }
}
