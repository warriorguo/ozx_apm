using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Network
{
    /// <summary>
    /// Records all network communication with the server for debugging purposes.
    /// Only active when LogLevel is Debug (ApmConfig.Default() picks Debug in
    /// development builds); otherwise every
    /// method returns immediately without building any strings. The log file is
    /// capped at MaxLogFileBytes (one rotated copy is kept) and is only written
    /// with consent.
    /// </summary>
    public class NetworkLogger
    {
        private readonly ApmConfig _config;
        private readonly string _logFilePath;
        private readonly object _lock = new object();
        private readonly List<NetworkLogEntry> _recentLogs = new List<NetworkLogEntry>();
        private readonly bool _enabled;

        private const int MaxRecentLogs = 100;
        private const string LogFileName = "ozx_apm_network.log";
        /// <summary>Size at which the log file is rotated to "&lt;name&gt;.1".</summary>
        public const long MaxLogFileBytes = 1024 * 1024;

        private long _fileBytes = -1; // unknown until the first write
        private bool _sessionMarkerWritten;

        public NetworkLogger(ApmConfig config)
            : this(config, Path.Combine(Application.persistentDataPath, LogFileName), false)
        {
        }

        /// <param name="forceEnable">Tests: enable regardless of LogLevel.</param>
        internal NetworkLogger(ApmConfig config, string logFilePath, bool forceEnable)
        {
            _config = config;
            _logFilePath = logFilePath;
            // Not tied to Debug.isDebugBuild: a development build with LogLevel.Warning
            // must not pay for per-request string building and main-thread file writes.
            _enabled = forceEnable || config.LogLevel >= LogLevel.Debug;
        }

        /// <summary>True with LogLevel.Debug.</summary>
        public bool IsEnabled => _enabled;

        /// <summary>
        /// Logs a request being sent to the server
        /// </summary>
        public void LogRequest(string url, string method, Dictionary<string, string> headers, string body, int bodyBytes, bool isCompressed)
        {
            if (!_enabled)
                return;

            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.Request,
                Url = url,
                Method = method,
                Headers = headers,
                Body = TruncateBody(body),
                BodyBytes = bodyBytes,
                IsCompressed = isCompressed,
                Message = $"REQUEST: {method} {url} ({bodyBytes} bytes{(isCompressed ? ", gzip" : "")})"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs a successful response from the server
        /// </summary>
        public void LogResponse(string url, int statusCode, string responseBody, double elapsedMs, int eventCount)
        {
            if (!_enabled)
                return;

            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.Response,
                Url = url,
                StatusCode = statusCode,
                Body = TruncateBody(responseBody),
                ElapsedMs = elapsedMs,
                EventCount = eventCount,
                Message = string.Format(CultureInfo.InvariantCulture,
                    "RESPONSE: {0} OK from {1} ({2:F0}ms, {3} events)", statusCode, url, elapsedMs, eventCount)
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs a failed request. retryInSeconds is when the next attempt is scheduled.
        /// </summary>
        public void LogFailure(string url, int statusCode, string error, string responseBody, double elapsedMs, int eventCount, int consecutiveFailures, float retryInSeconds)
        {
            if (!_enabled)
                return;

            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Error,
                Type = NetworkLogType.Failure,
                Url = url,
                StatusCode = statusCode,
                Error = error,
                Body = TruncateBody(responseBody),
                ElapsedMs = elapsedMs,
                EventCount = eventCount,
                ConsecutiveFailures = consecutiveFailures,
                BackoffMultiplier = retryInSeconds,
                Message = string.Format(CultureInfo.InvariantCulture,
                    "FAILURE: {0} from {1} (status: {2}, {3:F0}ms, failures: {4}, retry in {5:F1}s)",
                    error, url, statusCode, elapsedMs, consecutiveFailures, retryInSeconds)
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs when events are queued for offline storage
        /// </summary>
        public void LogOfflineQueue(int eventCount, string reason)
        {
            if (!_enabled)
                return;

            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Warning,
                Type = NetworkLogType.OfflineQueue,
                EventCount = eventCount,
                Message = $"OFFLINE_QUEUE: {eventCount} events queued - {reason}"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs when offline events are being retried
        /// </summary>
        public void LogOfflineRetry(int fileCount, int eventCount)
        {
            if (!_enabled)
                return;

            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Type = NetworkLogType.OfflineRetry,
                EventCount = eventCount,
                Message = $"OFFLINE_RETRY: Processing {fileCount} offline files ({eventCount} events)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs compression details
        /// </summary>
        public void LogCompression(int originalBytes, int compressedBytes)
        {
            if (!_enabled || originalBytes <= 0)
                return;

            float ratio = (float)compressedBytes / originalBytes;
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Debug,
                Type = NetworkLogType.Compression,
                BodyBytes = compressedBytes,
                Message = string.Format(CultureInfo.InvariantCulture,
                    "COMPRESSION: {0} -> {1} bytes ({2:P0})", originalBytes, compressedBytes, ratio)
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Gets recent log entries (in-memory cache). Empty unless IsEnabled.
        /// </summary>
        public List<NetworkLogEntry> GetRecentLogs()
        {
            lock (_lock)
            {
                return new List<NetworkLogEntry>(_recentLogs);
            }
        }

        /// <summary>
        /// Gets the full log file path
        /// </summary>
        public string GetLogFilePath()
        {
            return _logFilePath;
        }

        /// <summary>
        /// Clears the log file
        /// </summary>
        public void ClearLogs()
        {
            lock (_lock)
            {
                _recentLogs.Clear();
                try
                {
                    if (File.Exists(_logFilePath))
                        File.Delete(_logFilePath);
                    if (File.Exists(_logFilePath + ".1"))
                        File.Delete(_logFilePath + ".1");
                    _fileBytes = 0;
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Warning, $"Failed to clear network log file: {ex.Message}");
                }
            }
        }

        private void WriteLog(NetworkLogEntry entry)
        {
            lock (_lock)
            {
                // Add to recent logs cache
                _recentLogs.Add(entry);
                if (_recentLogs.Count > MaxRecentLogs)
                {
                    _recentLogs.RemoveAt(0);
                }

                // The log contains request bodies: no file without consent.
                if (!_config.TechnicalAllowed)
                    return;

                try
                {
                    var sb = new StringBuilder();
                    if (!_sessionMarkerWritten)
                    {
                        _sessionMarkerWritten = true;
                        sb.Append('[').Append(DateTime.UtcNow.ToString("yyyy-MM-dd HH:mm:ss.fff", CultureInfo.InvariantCulture))
                          .Append("] [Info] === Network logging session started === App: ").Append(_config.AppVersion)
                          .Append(", Server: ").Append(_config.ServerUrl).Append(Environment.NewLine);
                    }
                    sb.Append(FormatLogEntry(entry)).Append(Environment.NewLine);
                    string text = sb.ToString();

                    if (_fileBytes < 0)
                        _fileBytes = File.Exists(_logFilePath) ? new FileInfo(_logFilePath).Length : 0;
                    if (_fileBytes + text.Length > MaxLogFileBytes)
                    {
                        string rotated = _logFilePath + ".1";
                        if (File.Exists(rotated))
                            File.Delete(rotated);
                        if (File.Exists(_logFilePath))
                            File.Move(_logFilePath, rotated);
                        _fileBytes = 0;
                    }

                    File.AppendAllText(_logFilePath, text);
                    _fileBytes += Encoding.UTF8.GetByteCount(text);
                }
                catch (Exception ex)
                {
                    // Silently fail - don't want logging to break the SDK
                    ApmClient.Log(LogLevel.Debug, $"Failed to write network log: {ex.Message}");
                }
            }
        }

        private void LogToConsole(NetworkLogEntry entry)
        {
            if (_config.LogLevel < LogLevel.Debug)
                return;

            // Never Debug.LogError: upload failures are expected (offline players)
            // and an error-level log would be captured by ExceptionCollector and
            // by the game's own error reporting.
            string prefix = ApmClient.LogPrefix + " [Network] ";
            switch (entry.Level)
            {
                case NetworkLogLevel.Error:
                case NetworkLogLevel.Warning:
                    Debug.LogWarning(prefix + entry.Message);
                    break;
                default:
                    Debug.Log(prefix + entry.Message);
                    break;
            }
        }

        private string FormatLogEntry(NetworkLogEntry entry)
        {
            var sb = new StringBuilder();
            sb.Append('[').Append(entry.Timestamp.ToString("yyyy-MM-dd HH:mm:ss.fff", CultureInfo.InvariantCulture)).Append("] ");
            sb.Append('[').Append(entry.Level).Append("] ");
            sb.Append(entry.Message);

            // Add detailed info for requests and failures
            if (entry.Type == NetworkLogType.Request && entry.Headers != null)
            {
                sb.Append(" | Headers: ");
                foreach (var kv in entry.Headers)
                {
                    // Mask sensitive headers
                    string key = kv.Key.ToLowerInvariant();
                    string value = key.Contains("key") || key.Contains("auth")
                        ? MaskValue(kv.Value)
                        : kv.Value;
                    sb.Append(kv.Key).Append('=').Append(value).Append(", ");
                }
            }

            if (!string.IsNullOrEmpty(entry.Body) && (entry.Type == NetworkLogType.Request || entry.Type == NetworkLogType.Failure))
            {
                sb.Append(" | Body: ").Append(entry.Body);
            }

            if (!string.IsNullOrEmpty(entry.Error))
            {
                sb.Append(" | Error: ").Append(entry.Error);
            }

            return sb.ToString();
        }

        private string TruncateBody(string body)
        {
            if (string.IsNullOrEmpty(body))
                return null;

            const int maxLength = 1000;
            return body.Length > maxLength
                ? body.Substring(0, maxLength) + "... (truncated)"
                : body;
        }

        private string MaskValue(string value)
        {
            if (string.IsNullOrEmpty(value) || value.Length <= 4)
                return "****";
            return value.Substring(0, 4) + "****";
        }
    }

    /// <summary>
    /// Represents a single network log entry
    /// </summary>
    [Serializable]
    public class NetworkLogEntry
    {
        public DateTime Timestamp;
        public NetworkLogLevel Level;
        public NetworkLogType Type;
        public string Message;
        public string Url;
        public string Method;
        public Dictionary<string, string> Headers;
        public string Body;
        public int BodyBytes;
        public bool IsCompressed;
        public int StatusCode;
        public string Error;
        public double ElapsedMs;
        public int EventCount;
        public int ConsecutiveFailures;
        /// <summary>For failures: seconds until the next attempt.</summary>
        public float BackoffMultiplier;
    }

    public enum NetworkLogLevel
    {
        Debug,
        Info,
        Warning,
        Error
    }

    public enum NetworkLogType
    {
        Request,
        Response,
        Failure,
        OfflineQueue,
        OfflineRetry,
        Compression,
        Other
    }
}
