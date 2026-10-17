namespace OzxApm.Models
{
    /// <summary>
    /// Pool for the once-per-second PerfSampleEvent, so emitting a sample does not
    /// allocate on the main thread. The main thread rents; the storage worker
    /// returns the event once it is serialized to disk (or dropped). Only events
    /// that came from the pool go back into it, and they are fully reset when rented.
    /// </summary>
    internal static class PerfSamplePool
    {
        private const int Capacity = 64;
        private static readonly object s_lock = new object();
        private static readonly PerfSampleEvent[] s_items = new PerfSampleEvent[Capacity];
        private static int s_count;

        /// <summary>Main thread. Returns a reset event (allocates only when the pool is empty).</summary>
        public static PerfSampleEvent Rent()
        {
            PerfSampleEvent evt = null;
            lock (s_lock)
            {
                if (s_count > 0)
                {
                    evt = s_items[--s_count];
                    s_items[s_count] = null;
                }
            }
            if (evt == null)
                return new PerfSampleEvent { pooled = true };
            Reset(evt);
            return evt;
        }

        /// <summary>Any thread. Ignores events that were not rented from the pool.</summary>
        public static void Return(BaseEvent evt)
        {
            if (!(evt is PerfSampleEvent perf) || !perf.pooled)
                return;
            lock (s_lock)
            {
                if (s_count < Capacity)
                    s_items[s_count++] = perf;
            }
        }

        internal static int CountForTests
        {
            get
            {
                lock (s_lock)
                {
                    return s_count;
                }
            }
        }

        internal static void ClearForTests()
        {
            lock (s_lock)
            {
                for (int i = 0; i < s_count; i++)
                    s_items[i] = null;
                s_count = 0;
            }
        }

        private static void Reset(PerfSampleEvent evt)
        {
            // Every field: a stale event_id/seq/timestamp would be kept by the worker.
            evt.timestamp = 0;
            evt.event_id = null;
            evt.id_hi = 0;
            evt.id_lo = 0;
            evt.seq = 0;
            evt.app_version = null;
            evt.platform = null;
            evt.device_model = null;
            evt.os_version = null;
            evt.session_id = null;
            evt.device_id = null;
            evt.scene = null;
            evt.build_type = null;
            evt.gpu = null;
            evt.ram_class = null;
            evt.net_type = null;
            evt.ctx = null;
            evt.fps = 0;
            evt.frame_time_ms = 0;
            evt.main_thread_ms = 0;
            evt.gc_alloc_kb = 0;
            evt.mem_mb = 0;
        }
    }
}
