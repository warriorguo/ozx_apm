using OzxApm.Models;

namespace OzxApm.Core
{
    /// <summary>
    /// Snapshot of the SDK's internal state for diagnostics and validation.
    /// Reading it allocates nothing.
    /// </summary>
    public struct ApmStats
    {
        public bool Initialized;
        /// <summary>Technical consent (APM-14).</summary>
        public ConsentState Consent;
        /// <summary>Analytics consent (APM-14): the ops events of APM-12.</summary>
        public ConsentState AnalyticsConsent;
        /// <summary>Collectors are running (technical consent Granted).</summary>
        public bool Collecting;
        /// <summary>Events enqueued but not yet written by the storage worker.</summary>
        public int PendingEvents;
        /// <summary>Bytes of events stored locally and not yet confirmed by the server.</summary>
        public long StoredBytes;
        /// <summary>Events dropped locally (storage cap, in-memory overflow).</summary>
        public long DroppedEvents;
        /// <summary>Highest event sequence number assigned so far.</summary>
        public long LastSeq;
        /// <summary>Consecutive failed uploads (0 after a success).</summary>
        public int UploadFailures;
        public bool PersistsToDisk;
        public bool HasWorkerThread;
        /// <summary>Main-thread time of the last ApmClient.Update, in milliseconds.</summary>
        public double LastUpdateMs;
        /// <summary>Main-thread cost of the last upload start / completion, in milliseconds.</summary>
        public double LastUploadSendMs;
        public double LastUploadCompleteMs;
        /// <summary>Main-thread cost of the last SendWebRequest call / request dispose, in milliseconds.</summary>
        public double LastUploadStartMs;
        public double LastUploadDisposeMs;
        /// <summary>Game context keys currently set (ApmClient.SetContext).</summary>
        public int ContextKeys;
        /// <summary>SetContext calls refused (invalid key, too many keys, context too large).</summary>
        public int ContextRejected;
        /// <summary>Context values cut to GameContext.MaxValueLength.</summary>
        public int ContextTruncated;
        /// <summary>Error/exception/assert occurrences captured since Initialize (every repeat counted, APM-3).</summary>
        public long ErrorsSeen;
        /// <summary>Exception events enqueued for them (first occurrences and per-context repeat counts).</summary>
        public long ErrorEvents;
        /// <summary>Occurrences reported only in a summary event (more distinct errors per minute than tracked).</summary>
        public long ErrorsInSummary;
        /// <summary>TrackEvent calls recorded since Initialize (APM-6).</summary>
        public long CustomEvents;
        /// <summary>Of those, sent as counts beyond MaxCustomEventsPerMinute (without properties).</summary>
        public long CustomEventsAggregated;
        /// <summary>Of those, not sent (more distinct event names per minute than tracked); still on the time line.</summary>
        public long CustomEventsDropped;
        /// <summary>RecordMetric samples and stopped timers recorded since Initialize.</summary>
        public long MetricSamples;
        /// <summary>Samples not sent (more distinct metric names per minute than tracked).</summary>
        public long MetricSamplesDropped;
        /// <summary>Invalid event/metric names, property keys or values and NaN samples refused or cut.</summary>
        public int CustomRejected;
        /// <summary>Timers not recorded because the app was paused while they ran.</summary>
        public long TimersSpanningPause;
        /// <summary>StartTimer calls not remembered by name (more than 256 names pending: timers started and never stopped?).</summary>
        public long TimersUntracked;
        /// <summary>Warning/error/assert lines seen by the time line (repeats included).</summary>
        public long LogLinesSeen;
        /// <summary>Crash reporting is on (Android player, ApmConfig.EnableCrashReporting; APM-7).</summary>
        public bool CrashReporting;
        /// <summary>Crashes of earlier processes reported at this launch.</summary>
        public int CrashesReported;
        /// <summary>Earlier processes that ended without a crash (background kills, quits).</summary>
        public int ExitsNotCrashes;
        /// <summary>Crash journal writes in this process (its own thread).</summary>
        public long CrashJournalWrites;
        /// <summary>Duration of the last crash journal write on its thread, in milliseconds.</summary>
        public double CrashJournalWriteMs;
        /// <summary>The open game run's id (ApmClient.BeginRun, APM-9), or null.</summary>
        public string RunId;
        /// <summary>Runs ended in this process (EndRun, a new BeginRun, shutdown).</summary>
        public int RunsEnded;
        /// <summary>The perf sample reads the memory breakdown (ApmConfig.EnableMemoryBreakdown).</summary>
        public bool MemoryBreakdown;
        /// <summary>The process RSS is read from /proc/self/statm on its own thread (Android).</summary>
        public bool ProcRss;
        /// <summary>Main-thread time of the last memory reading of a perf sample, in milliseconds.</summary>
        public double MemorySampleMs;
        /// <summary>Time of the last RSS reading on its thread, in milliseconds.</summary>
        public double RssReadMs;
        /// <summary>The last RSS reading (MB); 0 before the first or where not read.</summary>
        public float RssMb;
        /// <summary>RSS readings so far.</summary>
        public long RssReadings;
        /// <summary>Janks detected since Initialize (APM-21), sent or not.</summary>
        public long JanksDetected;
        /// <summary>Of those, enqueued (within ApmConfig.MaxJanksPerMinute).</summary>
        public long JanksSent;
        /// <summary>Of those, not sent (beyond ApmConfig.MaxJanksPerMinute).</summary>
        public long JanksDropped;
        /// <summary>The expected frame time the jank detector compares with (average of recent normal frames, ms; 0 while it warms up).</summary>
        public float JankExpectedFrameMs;
        /// <summary>The last jank: duration_ms, max_frame_ms, frames in its span, slow frames among them.</summary>
        public float LastJankDurationMs;
        public float LastJankMaxFrameMs;
        public int LastJankFrames;
        public int LastJankSlowFrames;
        /// <summary>The jank detector's own time in its last sampled frame (one in 64), microseconds.</summary>
        public double JankUpdateUs;
        /// <summary>Two back-to-back Stopwatch reads in that frame (the self-check's own cost, already subtracted from JankUpdateUs), microseconds.</summary>
        public double JankSelfCheckOverheadUs;
        /// <summary>"release", "dev" or "editor"; null before Initialize.</summary>
        public string BuildType;
        /// <summary>False in the Unity Editor unless ApmConfig.UploadInEditor.</summary>
        public bool UploadsEnabled;
        /// <summary>APM-24: count checkpoint files written in this process (storage worker).</summary>
        public long CountCheckpoints;
        /// <summary>Size of the last count checkpoint written (bytes; 0 once deleted at a window close).</summary>
        public long CountCheckpointBytes;
        /// <summary>Count checkpoints of an earlier process found at this launch and queued for upload.</summary>
        public long CountCheckpointsRecovered;
        /// <summary>Pending counts left out of a checkpoint because it was full (they still go out with their window).</summary>
        public long CountCheckpointOmitted;

        /// <summary>APM-12: the ops queue exists (ApmConfig.EnableOpsEvents).</summary>
        public bool OpsEnabled;
        /// <summary>Ops events queued in this process (OpsEvent, ApmAds, session events).</summary>
        public long OpsEvents;
        /// <summary>Ops events queued but not yet written by the ops worker.</summary>
        public int OpsPendingEvents;
        /// <summary>Bytes of ops events stored locally and not yet confirmed by the server.</summary>
        public long OpsStoredBytes;
        /// <summary>Ops events dropped locally (only beyond ApmConfig.MaxOpsStorageBytes or the in-memory hand-off).</summary>
        public long OpsDroppedEvents;
        /// <summary>Highest ops sequence number assigned so far (the ops queue's own sequence).</summary>
        public long OpsLastSeq;
        /// <summary>Consecutive failed ops uploads (0 after a success).</summary>
        public int OpsUploadFailures;
        /// <summary>APM-13: the session file uploader runs (a development or editor build).</summary>
        public bool SessionFilesEnabled;
        /// <summary>Session file uploads waiting or under way.</summary>
        public int SessionFilesPending;
        /// <summary>Session file uploads completed / dropped in this process.</summary>
        public int SessionFilesCompleted;
        public int SessionFilesFailed;
        /// <summary>Session file bytes the server acknowledged in this process.</summary>
        public long SessionFileBytesSent;
        /// <summary>The last session file error (null: none).</summary>
        public string SessionFileLastError;
    }
}
