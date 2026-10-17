using System;
using System.Collections.Generic;

namespace OzxApm.Models
{
    [Serializable]
    public class BaseEvent
    {
        public string type;
        /// <summary>When the event happened (Unix ms, UTC). Never rewritten on flush or retry.</summary>
        public long timestamp;
        /// <summary>Client-generated unique id, assigned by the SDK before the event is persisted. Lets the server de-duplicate retries (APM-12).</summary>
        public string event_id;
        /// <summary>Per-install monotonic sequence number, persisted across launches. A gap means events were dropped locally.</summary>
        public long seq;
        public string app_version;
        public string platform;
        public string device_model;
        public string os_version;
        public string session_id;
        public string device_id;
        public string scene;
        /// <summary>"release", "dev" or "editor" (APM-5).</summary>
        public string build_type;
        public string gpu;
        public string ram_class;
        public string net_type;

        /// <summary>
        /// The game context when the event was enqueued (APM-5). Set by
        /// ApmClient.EnqueueEvent unless the collector already captured an earlier
        /// one (e.g. the first repeat of an exception). Immutable and shared.
        /// </summary>
        [NonSerialized] internal OzxApm.Core.ContextSnapshot ctx;

        /// <summary>Came from an event pool; returned to it after serialization.</summary>
        [NonSerialized] internal bool pooled;

        /// <summary>
        /// The event id as 128 bits, assigned by the storage worker when event_id
        /// is empty and written as its 32 hex digits: no string per event (APM-3).
        /// </summary>
        [NonSerialized] internal ulong id_hi;
        [NonSerialized] internal ulong id_lo;
    }

    [Serializable]
    public class PerfSampleEvent : BaseEvent
    {
        public float fps;
        public float frame_time_ms;
        public float main_thread_ms;
        public float gc_alloc_kb;
        public float mem_mb;

        public PerfSampleEvent()
        {
            type = "perf_sample";
        }
    }

    [Serializable]
    public class JankEvent : BaseEvent
    {
        public float duration_ms;
        public float max_frame_ms;
        public int recent_gc_count;
        public float recent_gc_alloc_kb;
        public List<string> recent_events;

        public JankEvent()
        {
            type = "jank";
            recent_events = new List<string>();
        }
    }

    [Serializable]
    public class StartupEvent : BaseEvent
    {
        public float phase1_ms;  // app -> unity
        public float phase2_ms;  // unity -> first frame
        public float tti_ms;     // first frame -> interactive

        public StartupEvent()
        {
            type = "startup";
        }
    }

    [Serializable]
    public class SceneLoadEvent : BaseEvent
    {
        public string scene_name;
        public float load_ms;
        public float activate_ms;

        public SceneLoadEvent()
        {
            type = "scene_load";
        }
    }

    [Serializable]
    public class ExceptionEvent : BaseEvent
    {
        /// <summary>The SDK's fingerprint (de-duplication on the device); the server recomputes its own (APM-4).</summary>
        public string fingerprint;
        public string message;
        public string stack;
        public int count;
        /// <summary>"exception", "error" or "assert" (APM-4).</summary>
        public string log_type;
        /// <summary>
        /// The exception found in an error log's message (Exception.ToString():
        /// type, message, stack, inner exceptions), extracted before the message
        /// is cut to 500 characters; empty if there is none (APM-4).
        /// </summary>
        public string embedded_exception;
        /// <summary>Breadcrumbs recorded before the first occurrence; null for repeat counts (APM-4).</summary>
        public List<string> breadcrumbs;
        /// <summary>
        /// Which error of its session the (first) occurrence this event reports
        /// was, counting every occurrence of every error: 1 is the session's first
        /// error (APM-3). 0: not set.
        /// </summary>
        public int error_seq;
        /// <summary>Milliseconds from the start of the session to that occurrence (APM-3).</summary>
        public long session_ms;
        /// <summary>
        /// Empty for a logged error; "domain" or "domain_terminating" for an
        /// exception no code caught (AppDomain.UnhandledException; terminating:
        /// the process is about to end), "task" for a faulted task nobody
        /// observed (TaskScheduler.UnobservedTaskException) (APM-3).
        /// </summary>
        public string unhandled;
        /// <summary>
        /// The count merges occurrences in different game contexts (more than the
        /// SDK keeps apart in one window); ctx then holds only the entries they
        /// all had (APM-3).
        /// </summary>
        public bool ctx_merged;

        public ExceptionEvent()
        {
            type = "exception";
        }
    }

    [Serializable]
    public class CrashEvent : BaseEvent
    {
        public string crash_type;
        public string fingerprint;
        public string stack;
        public List<string> breadcrumbs;

        public CrashEvent()
        {
            type = "crash";
            breadcrumbs = new List<string>();
        }
    }

    /// <summary>
    /// A custom event (ApmClient.TrackEvent, APM-6). Technical data: it only
    /// says what the game was doing (room entered, wave started) for
    /// diagnostics; business/operations events are APM-12.
    /// </summary>
    [Serializable]
    public class CustomEvent : BaseEvent
    {
        public string name;
        /// <summary>1 for one call; more for a count of calls beyond the per-window budget (aggregated).</summary>
        public int count;
        /// <summary>The count merges calls; their properties were not kept.</summary>
        public bool aggregated;
        /// <summary>The count merges calls in different game contexts; ctx holds only what they shared.</summary>
        public bool ctx_merged;
        /// <summary>Properties as key, value pairs (validated, immutable); null for none.</summary>
        [NonSerialized] internal string[] props;

        public CustomEvent()
        {
            type = "custom_event";
            count = 1;
        }
    }

    /// <summary>
    /// The samples of one custom metric (ApmClient.RecordMetric, timers) in one
    /// game context and one window (APM-6): count, sum, min, max and a
    /// log-bucketed histogram (Utils.MetricBuckets) from which the server
    /// computes percentiles.
    /// </summary>
    [Serializable]
    public class CustomMetricEvent : BaseEvent
    {
        public string name;
        /// <summary>"ms" for timers; what the game passed to RecordMetric otherwise (may be empty).</summary>
        public string unit;
        public long count;
        public double sum;
        public double min;
        public double max;
        /// <summary>Length of the window the samples were collected in (ms).</summary>
        public long window_ms;
        /// <summary>The bucket scheme (MetricBuckets.Version).</summary>
        public int hist_v;
        [NonSerialized] internal int[] hist_keys;
        [NonSerialized] internal long[] hist_counts;
        public bool ctx_merged;

        public CustomMetricEvent()
        {
            type = "custom_metric";
        }
    }

    [Serializable]
    public class EventBatch
    {
        public List<BaseEvent> events;

        public EventBatch()
        {
            events = new List<BaseEvent>();
        }
    }
}
