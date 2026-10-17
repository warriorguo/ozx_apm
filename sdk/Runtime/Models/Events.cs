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
        public string fingerprint;
        public string message;
        public string stack;
        public int count;

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
