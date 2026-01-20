using System;
using System.Collections.Generic;
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

        private class ExceptionInfo
        {
            public string Fingerprint;
            public string Message;
            public string Stack;
            public int Count;
            public float FirstSeen;
            public float LastSeen;
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

            string timestamp = DateTime.UtcNow.ToString("HH:mm:ss.fff");
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
            if (!_isActive)
                return;

            // Only capture errors and exceptions
            if (type != LogType.Error && type != LogType.Exception && type != LogType.Assert)
                return;

            string fingerprint = GenerateFingerprint(message, stackTrace);
            float now = Time.realtimeSinceStartup;

            if (_recentExceptions.TryGetValue(fingerprint, out var existing))
            {
                // Deduplicate - just increment count
                existing.Count++;
                existing.LastSeen = now;
            }
            else
            {
                // New exception
                if (_recentExceptions.Count >= MaxRecentExceptions)
                {
                    FlushExceptions();
                }

                _recentExceptions[fingerprint] = new ExceptionInfo
                {
                    Fingerprint = fingerprint,
                    Message = TruncateMessage(message, 500),
                    Stack = TruncateStack(stackTrace, 4000),
                    Count = 1,
                    FirstSeen = now,
                    LastSeen = now
                };
            }

            // Record as breadcrumb
            RecordBreadcrumb($"Exception: {TruncateMessage(message, 100)}");
        }

        private void FlushExceptions()
        {
            foreach (var kvp in _recentExceptions)
            {
                var info = kvp.Value;
                var evt = new ExceptionEvent
                {
                    fingerprint = info.Fingerprint,
                    message = info.Message,
                    stack = info.Stack,
                    count = info.Count
                };

                _client.EnqueueEvent(evt);
            }

            _recentExceptions.Clear();
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
