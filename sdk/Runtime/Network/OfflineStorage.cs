using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Network
{
    /// <summary>
    /// Handles offline storage and retry of failed event uploads
    /// </summary>
    public class OfflineStorage
    {
        private readonly ApmConfig _config;
        private readonly string _storagePath;
        private readonly object _lock = new object();

        private long _currentStorageSize;
        private const string FilePrefix = "ozx_apm_offline_";
        private const string FileExtension = ".json";

        public OfflineStorage(ApmConfig config)
        {
            _config = config;
            _storagePath = Path.Combine(Application.persistentDataPath, "ozx_apm_offline");

            EnsureStorageDirectory();
            CalculateStorageSize();
        }

        /// <summary>
        /// Stores events for later retry
        /// </summary>
        public void Store(List<BaseEvent> events)
        {
            if (!_config.EnableOfflineStorage || events == null || events.Count == 0)
                return;

            lock (_lock)
            {
                try
                {
                    string json = JsonSerializer.SerializeEventBatch(events);
                    byte[] data = Encoding.UTF8.GetBytes(json);

                    // Check size limits
                    if (_currentStorageSize + data.Length > _config.MaxOfflineStorageBytes)
                    {
                        // Remove oldest files to make room
                        CleanupOldFiles(data.Length);
                    }

                    // Write to file
                    string filename = $"{FilePrefix}{DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()}{FileExtension}";
                    string filepath = Path.Combine(_storagePath, filename);

                    File.WriteAllBytes(filepath, data);
                    _currentStorageSize += data.Length;

                    ApmClient.Log(LogLevel.Debug, $"Stored {events.Count} events offline ({data.Length} bytes)");
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Error, $"Failed to store events offline: {ex.Message}");
                }
            }
        }

        /// <summary>
        /// Processes stored offline events
        /// </summary>
        public void ProcessOfflineEvents(EventReporter reporter)
        {
            if (!_config.EnableOfflineStorage)
                return;

            lock (_lock)
            {
                try
                {
                    var files = GetOfflineFiles();
                    if (files.Length == 0)
                        return;

                    ApmClient.Log(LogLevel.Info, $"Processing {files.Length} offline event files");

                    foreach (var file in files)
                    {
                        try
                        {
                            string json = File.ReadAllText(file);
                            var events = DeserializeEvents(json);

                            if (events != null && events.Count > 0)
                            {
                                reporter.SendBatch(events);
                            }

                            // Delete file after processing
                            File.Delete(file);
                            _currentStorageSize -= new FileInfo(file).Length;
                        }
                        catch (Exception ex)
                        {
                            ApmClient.Log(LogLevel.Warning, $"Failed to process offline file: {ex.Message}");
                            // Delete corrupted file
                            try { File.Delete(file); } catch { }
                        }
                    }
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Error, $"Failed to process offline events: {ex.Message}");
                }
            }
        }

        /// <summary>
        /// Clears all offline storage
        /// </summary>
        public void Clear()
        {
            lock (_lock)
            {
                try
                {
                    var files = GetOfflineFiles();
                    foreach (var file in files)
                    {
                        File.Delete(file);
                    }
                    _currentStorageSize = 0;
                }
                catch (Exception ex)
                {
                    ApmClient.Log(LogLevel.Error, $"Failed to clear offline storage: {ex.Message}");
                }
            }
        }

        /// <summary>
        /// Gets the current storage size in bytes
        /// </summary>
        public long GetStorageSize()
        {
            return _currentStorageSize;
        }

        private void EnsureStorageDirectory()
        {
            if (!Directory.Exists(_storagePath))
            {
                Directory.CreateDirectory(_storagePath);
            }
        }

        private void CalculateStorageSize()
        {
            _currentStorageSize = 0;
            try
            {
                var files = GetOfflineFiles();
                foreach (var file in files)
                {
                    _currentStorageSize += new FileInfo(file).Length;
                }
            }
            catch
            {
                _currentStorageSize = 0;
            }
        }

        private string[] GetOfflineFiles()
        {
            if (!Directory.Exists(_storagePath))
                return Array.Empty<string>();

            return Directory.GetFiles(_storagePath, FilePrefix + "*" + FileExtension);
        }

        private void CleanupOldFiles(long bytesNeeded)
        {
            try
            {
                var files = GetOfflineFiles();
                // Sort by name (which contains timestamp) to get oldest first
                Array.Sort(files);

                long freedBytes = 0;
                foreach (var file in files)
                {
                    if (_currentStorageSize - freedBytes + bytesNeeded <= _config.MaxOfflineStorageBytes)
                        break;

                    long fileSize = new FileInfo(file).Length;
                    File.Delete(file);
                    freedBytes += fileSize;
                }

                _currentStorageSize -= freedBytes;
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Failed to cleanup old offline files: {ex.Message}");
            }
        }

        private List<BaseEvent> DeserializeEvents(string json)
        {
            // Simple parsing - extract events from the batch
            // In production, use a proper JSON parser
            try
            {
                // This is a simplified approach - in reality you'd want proper deserialization
                // For now, we'll just return null and let the caller handle it
                // The events were already serialized by our JsonSerializer, so they're in a known format

                // Parse the JSON manually to avoid Unity's JsonUtility limitations with polymorphism
                var events = new List<BaseEvent>();

                // Find the events array
                int eventsStart = json.IndexOf("[");
                int eventsEnd = json.LastIndexOf("]");

                if (eventsStart < 0 || eventsEnd < 0)
                    return events;

                string eventsJson = json.Substring(eventsStart + 1, eventsEnd - eventsStart - 1);

                // Split by },{ to get individual events (simplified)
                // This is a hack - in production use a proper JSON parser
                int depth = 0;
                int start = 0;

                for (int i = 0; i < eventsJson.Length; i++)
                {
                    char c = eventsJson[i];
                    if (c == '{') depth++;
                    else if (c == '}')
                    {
                        depth--;
                        if (depth == 0)
                        {
                            string eventJson = eventsJson.Substring(start, i - start + 1);
                            var evt = ParseEvent(eventJson);
                            if (evt != null)
                                events.Add(evt);
                            start = i + 2; // Skip },
                        }
                    }
                }

                return events;
            }
            catch
            {
                return new List<BaseEvent>();
            }
        }

        private BaseEvent ParseEvent(string json)
        {
            try
            {
                // Extract type field
                int typeStart = json.IndexOf("\"type\":\"") + 8;
                int typeEnd = json.IndexOf("\"", typeStart);
                string type = json.Substring(typeStart, typeEnd - typeStart);

                // Use Unity's JsonUtility based on type
                switch (type)
                {
                    case "perf_sample":
                        return JsonUtility.FromJson<PerfSampleEvent>(json);
                    case "jank":
                        return JsonUtility.FromJson<JankEvent>(json);
                    case "startup":
                        return JsonUtility.FromJson<StartupEvent>(json);
                    case "scene_load":
                        return JsonUtility.FromJson<SceneLoadEvent>(json);
                    case "exception":
                        return JsonUtility.FromJson<ExceptionEvent>(json);
                    case "crash":
                        return JsonUtility.FromJson<CrashEvent>(json);
                    default:
                        return null;
                }
            }
            catch
            {
                return null;
            }
        }
    }

    /// <summary>
    /// Records all network communication with the server for debugging purposes.
    /// Logs are persisted to file and can be retrieved for analysis.
    /// </summary>
    public class NetworkLogger
    {
        private readonly ApmConfig _config;
        private readonly string _logFilePath;
        private readonly object _lock = new object();
        private readonly List<NetworkLogEntry> _recentLogs = new List<NetworkLogEntry>();

        private const int MaxRecentLogs = 100;
        private const string LogFileName = "ozx_apm_network.log";

        public NetworkLogger(ApmConfig config)
        {
            _config = config;
            _logFilePath = Path.Combine(Application.persistentDataPath, LogFileName);

            // Output log file path to console
            Debug.Log($"[OzxApm] Network log file: {_logFilePath}");

            // Write session start marker
            WriteLog(new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Info,
                Message = $"=== Network logging session started === App: {config.AppVersion}, Server: {config.ServerUrl}"
            });
        }

        /// <summary>
        /// Logs a request being sent to the server
        /// </summary>
        public void LogRequest(string url, string method, Dictionary<string, string> headers, string body, int bodyBytes, bool isCompressed)
        {
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
                Message = $"RESPONSE: {statusCode} OK from {url} ({elapsedMs:F0}ms, {eventCount} events)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs a failed request
        /// </summary>
        public void LogFailure(string url, int statusCode, string error, string responseBody, double elapsedMs, int eventCount, int consecutiveFailures, float backoffMultiplier)
        {
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
                BackoffMultiplier = backoffMultiplier,
                Message = $"FAILURE: {error} from {url} (status: {statusCode}, {elapsedMs:F0}ms, failures: {consecutiveFailures}, backoff: {backoffMultiplier}x)"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Logs when events are queued for offline storage
        /// </summary>
        public void LogOfflineQueue(int eventCount, string reason)
        {
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
            float ratio = (float)compressedBytes / originalBytes;
            var entry = new NetworkLogEntry
            {
                Timestamp = DateTime.UtcNow,
                Level = NetworkLogLevel.Debug,
                Type = NetworkLogType.Compression,
                BodyBytes = compressedBytes,
                Message = $"COMPRESSION: {originalBytes} -> {compressedBytes} bytes ({ratio:P0})"
            };

            WriteLog(entry);
            LogToConsole(entry);
        }

        /// <summary>
        /// Gets recent log entries (in-memory cache)
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
                    {
                        File.Delete(_logFilePath);
                    }
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

                // Only write to file in debug builds or if log level is Debug
                if (!Debug.isDebugBuild && _config.LogLevel > LogLevel.Debug)
                    return;

                try
                {
                    string logLine = FormatLogEntry(entry);
                    File.AppendAllText(_logFilePath, logLine + Environment.NewLine);
                }
                catch (Exception ex)
                {
                    // Silently fail - don't want logging to break the SDK
                    Debug.LogWarning($"[OzxApm] Failed to write network log: {ex.Message}");
                }
            }
        }

        private void LogToConsole(NetworkLogEntry entry)
        {
            if (_config.LogLevel > LogLevel.Debug)
                return;

            string prefix = "[OzxApm] [Network] ";
            switch (entry.Level)
            {
                case NetworkLogLevel.Error:
                    Debug.LogError(prefix + entry.Message);
                    break;
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
            sb.Append($"[{entry.Timestamp:yyyy-MM-dd HH:mm:ss.fff}] ");
            sb.Append($"[{entry.Level}] ");
            sb.Append(entry.Message);

            // Add detailed info for requests and failures
            if (entry.Type == NetworkLogType.Request && entry.Headers != null)
            {
                sb.Append(" | Headers: ");
                foreach (var kv in entry.Headers)
                {
                    // Mask sensitive headers
                    string value = kv.Key.ToLower().Contains("key") || kv.Key.ToLower().Contains("auth")
                        ? MaskValue(kv.Value)
                        : kv.Value;
                    sb.Append($"{kv.Key}={value}, ");
                }
            }

            if (!string.IsNullOrEmpty(entry.Body) && (entry.Type == NetworkLogType.Request || entry.Type == NetworkLogType.Failure))
            {
                sb.Append($" | Body: {entry.Body}");
            }

            if (!string.IsNullOrEmpty(entry.Error))
            {
                sb.Append($" | Error: {entry.Error}");
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
