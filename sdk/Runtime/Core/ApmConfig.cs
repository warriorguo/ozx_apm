using System;
using OzxApm.Models;

namespace OzxApm.Core
{
    [Serializable]
    public class ApmConfig
    {
        /// <summary>
        /// Server endpoint URL for event reporting
        /// </summary>
        public string ServerUrl { get; set; } = "http://localhost:8080";

        /// <summary>
        /// Application key for authentication
        /// </summary>
        public string AppKey { get; set; } = "";

        /// <summary>
        /// Application version string
        /// </summary>
        public string AppVersion { get; set; } = "1.0.0";

        /// <summary>
        /// Build number or identifier
        /// </summary>
        public string Build { get; set; } = "";

        /// <summary>
        /// Enable/disable the SDK
        /// </summary>
        public bool Enabled { get; set; } = true;

        /// <summary>
        /// Enable performance sampling
        /// </summary>
        public bool EnablePerformance { get; set; } = true;

        /// <summary>
        /// Enable jank detection
        /// </summary>
        public bool EnableJankDetection { get; set; } = true;

        /// <summary>
        /// Enable exception capture
        /// </summary>
        public bool EnableExceptionCapture { get; set; } = true;

        /// <summary>
        /// Enable startup timing
        /// </summary>
        public bool EnableStartupTiming { get; set; } = true;

        /// <summary>
        /// Enable scene load tracking
        /// </summary>
        public bool EnableSceneLoadTracking { get; set; } = true;

        /// <summary>
        /// Performance sampling interval in seconds
        /// </summary>
        public float SamplingIntervalSeconds { get; set; } = 1.0f;

        /// <summary>
        /// Frame time threshold for jank detection (ms)
        /// </summary>
        public float JankThresholdMs { get; set; } = 50.0f;

        /// <summary>
        /// Sustained jank threshold (ms)
        /// </summary>
        public float SustainedJankThresholdMs { get; set; } = 33.0f;

        /// <summary>
        /// Maximum events to batch before sending
        /// </summary>
        public int BatchSize { get; set; } = 20;

        /// <summary>
        /// Maximum batch size in bytes
        /// </summary>
        public int MaxBatchBytes { get; set; } = 64 * 1024;

        /// <summary>
        /// Flush interval in seconds
        /// </summary>
        public float FlushIntervalSeconds { get; set; } = 30.0f;

        /// <summary>
        /// Maximum events in memory queue
        /// </summary>
        public int MaxQueueSize { get; set; } = 1000;

        /// <summary>
        /// Enable offline storage for failed uploads
        /// </summary>
        public bool EnableOfflineStorage { get; set; } = true;

        /// <summary>
        /// Maximum offline storage size in bytes
        /// </summary>
        public int MaxOfflineStorageBytes { get; set; } = 5 * 1024 * 1024;

        /// <summary>
        /// Request timeout in seconds
        /// </summary>
        public float RequestTimeoutSeconds { get; set; } = 30.0f;

        /// <summary>
        /// Maximum retry attempts for failed requests
        /// </summary>
        public int MaxRetryAttempts { get; set; } = 3;

        /// <summary>
        /// Enable gzip compression for requests
        /// </summary>
        public bool EnableCompression { get; set; } = true;

        /// <summary>
        /// Log level for SDK internal logging
        /// </summary>
        public LogLevel LogLevel { get; set; } = LogLevel.Warning;

        /// <summary>
        /// Optional user ID for correlation
        /// </summary>
        public string UserId { get; set; } = "";

        /// <summary>
        /// Creates a default configuration
        /// </summary>
        public static ApmConfig Default()
        {
            return new ApmConfig();
        }

        /// <summary>
        /// Creates a minimal configuration with reduced overhead
        /// </summary>
        public static ApmConfig Minimal()
        {
            return new ApmConfig
            {
                EnablePerformance = false,
                EnableJankDetection = false,
                EnableStartupTiming = false,
                EnableSceneLoadTracking = false,
                SamplingIntervalSeconds = 5.0f,
                FlushIntervalSeconds = 60.0f
            };
        }
    }
}
