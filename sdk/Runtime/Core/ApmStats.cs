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
        public ConsentState Consent;
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
        /// <summary>"release", "dev" or "editor"; null before Initialize.</summary>
        public string BuildType;
        /// <summary>False in the Unity Editor unless ApmConfig.UploadInEditor.</summary>
        public bool UploadsEnabled;
    }
}
