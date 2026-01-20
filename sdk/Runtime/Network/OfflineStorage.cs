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
}
