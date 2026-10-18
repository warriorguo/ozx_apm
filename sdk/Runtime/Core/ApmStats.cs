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
        /// <summary>Analytics consent (APM-14; no analytics events are collected yet).</summary>
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
        /// <summary>"release", "dev" or "editor"; null before Initialize.</summary>
        public string BuildType;
        /// <summary>False in the Unity Editor unless ApmConfig.UploadInEditor.</summary>
        public bool UploadsEnabled;
    }
}
