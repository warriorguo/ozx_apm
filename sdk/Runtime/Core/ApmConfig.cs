using System;
using UnityEngine;
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
        /// Persist events to disk as soon as they are enqueued and keep them until
        /// the server confirms receipt (survives crashes, kills and offline sessions).
        /// When false, events are queued in memory only and are lost on exit.
        /// </summary>
        public bool EnableOfflineStorage { get; set; } = true;

        /// <summary>
        /// Maximum local event storage in bytes. When full, the oldest events are dropped.
        /// </summary>
        public int MaxOfflineStorageBytes { get; set; } = 5 * 1024 * 1024;

        /// <summary>
        /// How often the background writer appends newly enqueued events to disk (ms).
        /// This bounds how many events can be lost on a crash or kill. Exceptions and
        /// crashes are written immediately.
        /// </summary>
        public int PersistIntervalMs { get; set; } = 200;

        /// <summary>
        /// First retry delay after a failed upload (seconds). Doubles per consecutive failure.
        /// </summary>
        public float RetryBaseDelaySeconds { get; set; } = 5.0f;

        /// <summary>
        /// Upper bound for the retry delay (seconds). Uploads are retried at least this
        /// often for as long as there is unsent data; nothing is discarded for failing.
        /// </summary>
        public float RetryMaxDelaySeconds { get; set; } = 300.0f;

        /// <summary>
        /// Player consent. Nothing is enqueued, written to disk or uploaded unless Granted.
        /// Defaults to Granted (pre-APM-14 behaviour). Change at runtime with ApmClient.SetConsent.
        /// </summary>
        public ConsentState Consent { get; set; } = ConsentState.Granted;

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
        /// Creates a default configuration.
        /// In Debug builds, LogLevel defaults to Debug to enable network logging.
        /// In Release builds, LogLevel defaults to Warning.
        /// </summary>
        public static ApmConfig Default()
        {
            return new ApmConfig
            {
                // Enable Debug level logging in development builds
                LogLevel = Debug.isDebugBuild ? LogLevel.Debug : LogLevel.Warning
            };
        }

        /// <summary>
        /// Creates a minimal configuration with reduced overhead.
        /// In Debug builds, LogLevel defaults to Debug to enable network logging.
        /// In Release builds, LogLevel defaults to Warning.
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
                FlushIntervalSeconds = 60.0f,
                // Enable Debug level logging in development builds
                LogLevel = Debug.isDebugBuild ? LogLevel.Debug : LogLevel.Warning
            };
        }
    }
}
