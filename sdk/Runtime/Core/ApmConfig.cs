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
        /// Build type sent with every event: Auto (default) detects Editor / Dev
        /// (development build) / Release. Set it to override, e.g. Dev for an
        /// internal test build made without the Development Build flag.
        /// </summary>
        public ApmBuildType BuildType { get; set; } = ApmBuildType.Auto;

        /// <summary>
        /// Upload from the Unity Editor. Off by default: Editor play sessions are
        /// collected and stored locally (and visible in ApmClient.GetStats) but not
        /// sent. Events from the Editor carry build_type "editor" either way.
        /// </summary>
        public bool UploadInEditor { get; set; } = false;

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
        /// Custom events, metrics and timers (ApmClient.TrackEvent,
        /// RecordMetric, StartTimer/StopTimer; APM-6). When off those calls do
        /// nothing. Technical data: collected only with technical consent.
        /// </summary>
        public bool EnableCustomEvents { get; set; } = true;

        /// <summary>
        /// Custom events sent one by one (with their properties) per minute;
        /// further calls in the same minute are sent as one count per event name
        /// and game context, without properties. Every call is on the breadcrumb
        /// time line either way.
        /// </summary>
        public int MaxCustomEventsPerMinute { get; set; } = 60;

        /// <summary>
        /// Of MaxCustomEventsPerMinute, at most this many of one event name per
        /// minute (so one frequent name cannot use up the budget of the others;
        /// APM-10: a game that exports sessions in AiTelemetry's format, where
        /// e.g. every hit the player takes is a line, raises both). Values
        /// below 1 read as 1.
        /// </summary>
        public int MaxCustomEventsPerNamePerMinute { get; set; } = 10;

        /// <summary>
        /// Native crashes, ANRs and kills by the system (APM-7), Android player
        /// only: a crash journal (session, context, breadcrumbs, recent warnings
        /// and errors, scene, foreground) is kept on disk while the game runs,
        /// and the next launch reports how the previous process ended
        /// (ApplicationExitInfo on Android 11+, with the native stack on 12+ and
        /// the ANR trace on 11+). Technical data: only with technical consent.
        /// </summary>
        public bool EnableCrashReporting { get; set; } = true;

        /// <summary>
        /// ANR watchdog (APM-7; Android, with EnableCrashReporting): reports an
        /// ANR while the game runs (most ANRs do not end the process), not only
        /// at the next launch. A background thread queues a marker message on
        /// the Java main thread once a second in the foreground; when it waits
        /// more than 5 s and Android has declared the process "not responding",
        /// the ANR is reported with the main thread's Java stack.
        /// </summary>
        public bool EnableAnrWatchdog { get; set; } = true;

        /// <summary>
        /// Performance sampling interval in seconds
        /// </summary>
        public float SamplingIntervalSeconds { get; set; } = 1.0f;

        /// <summary>
        /// The perf sample's memory breakdown (APM-9): Unity's managed heap
        /// reserved, native memory allocated and reserved, graphics memory, and
        /// the process RSS (Android: /proc/self/statm on a thread of its own at
        /// SamplingIntervalSeconds; elsewhere Unity's System Used Memory), and
        /// the peaks of game runs (ApmClient.BeginRun). Graphics memory only in
        /// development players and the Editor (Unity keeps it nowhere else). Read once per perf
        /// sample, never per frame.
        /// </summary>
        public bool EnableMemoryBreakdown { get; set; } = true;

        /// <summary>
        /// A jank (APM-21) is a run of consecutive slow frames lasting more than
        /// this (ms): a single slow frame over it, or several shorter ones in a
        /// row. A frame is slow when it is longer than max(1.5 x, x + 8 ms) of the
        /// expected frame time x (the average of the recent normal frames), so a
        /// game running at 30 fps or below its target is never janky for that
        /// alone. Default 50.
        /// </summary>
        public float JankThresholdMs { get; set; } = 50.0f;

        /// <summary>
        /// Ignored since APM-21: a fixed sustained threshold made every frame of a
        /// 30 fps game "sustained" (a jank never ended). Slow frames are now
        /// relative to the frame rate (see JankThresholdMs).
        /// </summary>
        [System.Obsolete("Ignored since APM-21: slow frames are relative to the frame rate (see JankThresholdMs).")]
        public float SustainedJankThresholdMs { get; set; } = 33.0f;

        /// <summary>
        /// Janks sent per minute at most (APM-21); further janks of the minute
        /// are counted in ApmStats.JanksDropped, not sent. Janks less than
        /// 250 ms apart are already one jank. Default 20.
        /// </summary>
        public int MaxJanksPerMinute { get; set; } = 20;

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
        /// APM-24: how often (at most) the counts that wait in memory until
        /// their one-minute window closes (repeats of an exception, custom events
        /// beyond the per-minute budget, custom metric samples) are written to a
        /// checkpoint file by the storage worker, and only after something was
        /// counted. If the process dies in the foreground (crash, kill, OOM) the
        /// next launch uploads them, so at most this many seconds of counts are
        /// lost instead of the whole minute. Clamped to 1..60 s; 0 (or less)
        /// turns checkpoints off. Off the main thread; one small file per
        /// collector rewritten in place of the previous one (a large one less
        /// often: at most ~4 KB/s on average). Default 5 s.
        /// </summary>
        public float CountCheckpointIntervalSeconds { get; set; } = 5.0f;

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
        /// Technical consent (crashes, exceptions, performance) until the player
        /// makes a choice with ApmClient.SetConsent. A stored choice (RememberConsent)
        /// wins over this default. Granted by default (markets where diagnostics
        /// need no opt-in); set it to Undecided where collection needs consent first.
        /// </summary>
        public ConsentState DefaultTechnicalConsent { get; set; } = ConsentState.Granted;

        /// <summary>
        /// Analytics (operations) consent until the player makes a choice.
        /// Undecided by default: nothing is collected until the game asks.
        /// </summary>
        public ConsentState DefaultAnalyticsConsent { get; set; } = ConsentState.Undecided;

        // ------------------------------------------------------------------
        // Operations events (APM-12)
        // ------------------------------------------------------------------

        /// <summary>
        /// The ops queue (ApmClient.OpsEvent, ApmAds, the automatic session
        /// events): business events for designers and operations (retention,
        /// funnels, ads), never sampled, rate limited or aggregated, stored on
        /// disk until the server confirms them. Collected only with Analytics
        /// consent. Off: nothing of it runs (no thread, no directory).
        /// </summary>
        public bool EnableOpsEvents { get; set; } = true;

        /// <summary>
        /// Send "apm_session_start" when a session starts (a launch, or a return
        /// after a long background) and "apm_session_end" with the foreground
        /// time when the app goes to the background or quits, as ops events:
        /// what DAU, new players, retention and session length are counted from.
        /// </summary>
        public bool OpsSessionEvents { get; set; } = true;

        /// <summary>
        /// The store or channel the game was installed from (e.g. "googleplay",
        /// "appstore", "huawei"; 1-64 characters from [A-Za-z0-9_.-], else
        /// left out): sent with every ops event and kept as the player's install
        /// channel. Set it per build flavor.
        /// </summary>
        public string Channel { get; set; } = "";

        /// <summary>
        /// The player's country (ISO 3166-1 alpha-2, e.g. "DE") for ops events.
        /// Null: the device's region setting where the SDK can read it (Android,
        /// once at Initialize), else none. Coarse by design: never a location.
        /// The server prefers the country a trusted CDN header reports.
        /// </summary>
        public string Country { get; set; } = null;

        /// <summary>
        /// The game context keys (ApmClient.SetContext) an ops event carries;
        /// the rest of the context never leaves the device with ops events. The
        /// server keeps only the keys of its own whitelist (ops.context_keys,
        /// the same list by default): to add a key, add it on both sides. Null:
        /// the whole context is sent (the server still keeps only its whitelist).
        /// </summary>
        public string[] OpsContextKeys { get; set; } = { "runId", "chapter", "levelId", "room", "roomId", "revivesUsed" };

        /// <summary>
        /// Disk the ops queue may use (bytes); beyond it the oldest stored ops
        /// events are dropped (counted in ApmStats.OpsDroppedEvents). About
        /// 300-600 bytes per event. The only other loss: more than
        /// MaxQueueSize (1000) ops events sent within one write interval
        /// (PersistIntervalMs) of the ops worker, or a disk that keeps failing.
        /// </summary>
        public int MaxOpsStorageBytes { get; set; } = 4 * 1024 * 1024;

        // ------------------------------------------------------------------
        // Session files (APM-13): AiTelemetry session directories uploaded by
        // development builds (ApmSessionFiles.Upload). Compiled out of release
        // players and refused at run time in them whatever is set here.

        /// <summary>
        /// Allow ApmSessionFiles.Upload in development and editor builds (it is
        /// always a no-op in release builds). On by default; it does nothing until
        /// the game calls Upload.
        /// </summary>
        public bool EnableSessionFileUpload { get; set; } = true;

        /// <summary>
        /// The server's session file upload key (OZX_SESSION_FILES_UPLOAD_KEY,
        /// RELEASE.md APM-13). Set it only in development builds (e.g. from the
        /// build script); without the right key the server refuses (401) and
        /// the recordings wait on the device.
        /// </summary>
        public string SessionFilesUploadKey { get; set; } = "";

        /// <summary>Send session files only on Wi-Fi / LAN (not on mobile data). Default on.</summary>
        public bool SessionFilesWifiOnly { get; set; } = true;

        /// <summary>Average upload rate of session files, bytes per second (0: unlimited). Default 512 KB/s.</summary>
        public int SessionFilesBytesPerSecond { get; set; } = 512 * 1024;

        /// <summary>Size of one uploaded part (64 KB - 4 MB). Default 512 KB: below proxies' body limits, resumable in small steps.</summary>
        public int SessionFilePartBytes { get; set; } = 512 * 1024;

        /// <summary>Disk the zipped recordings waiting for upload may use. Default 512 MB.</summary>
        public long MaxSessionFileStorageBytes { get; set; } = 512L * 1024 * 1024;

        /// <summary>Uploads that may wait at once; Upload refuses more. Default 10.</summary>
        public int MaxPendingSessionFiles { get; set; } = 10;

        /// <summary>A recording not uploaded within this many days is dropped from the queue. Default 14.</summary>
        public int SessionFileMaxAgeDays { get; set; } = 14;

        /// <summary>
        /// Store the player's choice (SetConsent) in PlayerPrefs and use it on the
        /// next launch instead of the defaults. Turn it off if the game keeps the
        /// choice itself and calls ApmClient.SetConsent at every launch.
        /// </summary>
        public bool RememberConsent { get; set; } = true;

        /// <summary>
        /// Opt-in preset (e.g. mainland China): both categories Undecided, so the
        /// SDK collects, stores and uploads nothing until the game calls
        /// ApmClient.SetConsent. Returns this config.
        /// </summary>
        public ApmConfig UseOptInConsent()
        {
            DefaultTechnicalConsent = ConsentState.Undecided;
            DefaultAnalyticsConsent = ConsentState.Undecided;
            return this;
        }

        /// <summary>
        /// Pre-APM-14 single consent: reads the technical state; setting it sets
        /// both defaults and the current state of both categories. Prefer the
        /// Default*Consent properties and ApmClient.SetConsent(category, state).
        /// </summary>
        public ConsentState Consent
        {
            get => GetConsent(ConsentCategory.Technical);
            set
            {
                DefaultTechnicalConsent = value;
                DefaultAnalyticsConsent = value;
                SetConsentState(ConsentCategory.Technical, value);
                SetConsentState(ConsentCategory.Analytics, value);
            }
        }

        // Current state per category, read from any thread (storage worker,
        // log callbacks). -1: not set yet, the default applies.
        private int _technicalConsent = -1;
        private int _analyticsConsent = -1;

        /// <summary>Current consent of a category (the default until one is set).</summary>
        internal ConsentState GetConsent(ConsentCategory category)
        {
            int value = category == ConsentCategory.Analytics
                ? System.Threading.Volatile.Read(ref _analyticsConsent)
                : System.Threading.Volatile.Read(ref _technicalConsent);
            if (value >= 0)
                return (ConsentState)value;
            return category == ConsentCategory.Analytics ? DefaultAnalyticsConsent : DefaultTechnicalConsent;
        }

        internal void SetConsentState(ConsentCategory category, ConsentState state)
        {
            if (category == ConsentCategory.Analytics)
                System.Threading.Volatile.Write(ref _analyticsConsent, (int)state);
            else
                System.Threading.Volatile.Write(ref _technicalConsent, (int)state);
        }

        /// <summary>
        /// True while technical data may be collected, stored and uploaded. One
        /// volatile read: checked on every enqueue, write and upload.
        /// </summary>
        internal bool TechnicalAllowed => GetConsent(ConsentCategory.Technical) == ConsentState.Granted;

        /// <summary>True while ops events may be collected, stored and uploaded (APM-12).</summary>
        internal bool AnalyticsAllowed => GetConsent(ConsentCategory.Analytics) == ConsentState.Granted;

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
