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
        /// When false, events are queued in memory only (at most 1MB) and are lost on exit.
        /// WebGL is not a supported target: there the SDK always runs memory-only,
        /// without threads, whatever this is set to.
        /// </summary>
        public bool EnableOfflineStorage { get; set; } = true;

        /// <summary>
        /// Maximum local event storage in bytes. When full, the oldest events are dropped.
        /// </summary>
        public int MaxOfflineStorageBytes { get; set; } = 5 * 1024 * 1024;

        /// <summary>
        /// Upper bound (ms) for how long an enqueued event waits before the background
        /// writer appends it to disk, and so for how much can be lost on a crash or
        /// kill. The writer wakes immediately when the queue goes from empty to
        /// non-empty and for exceptions/crashes; this interval only coalesces bursts.
        /// Without a worker thread it is also the main-thread write interval.
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
        /// Uploads (including the backlog stored by earlier sessions) start this many
        /// seconds after Initialize, or earlier when the game calls ApmClient.MarkTTI
        /// or ApmClient.Flush, so they do not compete with the game's own loading.
        /// Events are still written to disk right away.
        /// </summary>
        public float InitialUploadDelaySeconds { get; set; } = 10.0f;

        /// <summary>
        /// Minimum time between two upload requests (seconds). Paces the drain of
        /// a stored backlog (about one request per second by default).
        /// </summary>
        public float UploadIntervalSeconds { get; set; } = 1.0f;

        /// <summary>
        /// Average upload rate (uncompressed bytes per second) while a stored backlog
        /// is drained. The backlog goes out in merged requests of up to MaxBatchBytes,
        /// spaced by their size: a 64KB request is followed by an 8 s pause at the
        /// default, so a full 5MB backlog drains in about 11 minutes and each request's
        /// main-thread work stays in well under 1% of frames.
        /// </summary>
        public int UploadBytesPerSecond { get; set; } = 8 * 1024;

        /// <summary>
        /// How long low-value data (perf samples, jank, scene loads, network and asset
        /// timings) written during this session may wait before it is uploaded
        /// (seconds). Every upload request costs a few main-thread frames, so while the
        /// game idles this data goes out in one request per interval instead of one
        /// every ~20 s. Exceptions, crashes and startup events are never delayed by it.
        /// Held data goes out earlier when a full request (MaxBatchBytes) is waiting,
        /// when any other request is sent, at once on pause, background, quit and
        /// ApmClient.Flush, and with the first upload of the process (after
        /// InitialUploadDelaySeconds or MarkTTI, so the one-time cost of the first
        /// request stays in the loading phase). It is on disk while it waits, so
        /// nothing is lost on a crash.
        /// Maximum delay of perf data while online: this interval plus about one second
        /// (plus the retry backoff if uploads fail). Clamped to 0..600 s; 0 disables
        /// the hold (one request per sealed segment, the pre-APM-2 cadence).
        /// </summary>
        public float LowValueUploadIntervalSeconds { get; set; } = 120.0f;

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
