using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using OzxApm.Models;

namespace OzxApm.Utils
{
    /// <summary>
    /// Minimal JSON serializer to avoid GC allocations from Unity's JsonUtility
    /// </summary>
    public static class JsonSerializer
    {
        private static readonly StringBuilder _builder = new StringBuilder(4096);
        private static readonly object _lock = new object();

        /// <summary>
        /// Serializes an event batch to JSON
        /// </summary>
        public static string SerializeEventBatch(List<BaseEvent> events)
        {
            lock (_lock)
            {
                _builder.Clear();
                _builder.Append("{\"events\":[");

                for (int i = 0; i < events.Count; i++)
                {
                    if (i > 0) _builder.Append(',');
                    SerializeEvent(_builder, events[i]);
                }

                _builder.Append("]}");
                return _builder.ToString();
            }
        }

        /// <summary>
        /// Serializes a single event to one line of JSON (no newlines inside).
        /// This is both the on-disk record and the element of the upload batch.
        /// Output does not depend on the current culture.
        /// </summary>
        public static string SerializeEvent(BaseEvent evt)
        {
            lock (_lock)
            {
                _builder.Clear();
                SerializeEvent(_builder, evt);
                return _builder.ToString();
            }
        }

        private static void SerializeEvent(StringBuilder sb, BaseEvent evt)
        {
            sb.Append('{');

            // Common fields
            AppendField(sb, "type", evt.type, true);
            AppendField(sb, "event_id", evt.event_id);
            AppendField(sb, "seq", evt.seq);
            AppendField(sb, "timestamp", evt.timestamp);
            AppendField(sb, "app_version", evt.app_version);
            AppendField(sb, "platform", evt.platform);
            AppendField(sb, "device_model", evt.device_model);
            AppendField(sb, "os_version", evt.os_version);
            AppendField(sb, "session_id", evt.session_id);
            AppendField(sb, "device_id", evt.device_id);
            if (!string.IsNullOrEmpty(evt.scene))
                AppendField(sb, "scene", evt.scene);
            if (!string.IsNullOrEmpty(evt.build_type))
                AppendField(sb, "build_type", evt.build_type);
            if (!string.IsNullOrEmpty(evt.gpu))
                AppendField(sb, "gpu", evt.gpu);
            if (!string.IsNullOrEmpty(evt.ram_class))
                AppendField(sb, "ram_class", evt.ram_class);
            if (!string.IsNullOrEmpty(evt.net_type))
                AppendField(sb, "net_type", evt.net_type);

            // Type-specific fields
            switch (evt)
            {
                case PerfSampleEvent perf:
                    AppendField(sb, "fps", perf.fps);
                    AppendField(sb, "frame_time_ms", perf.frame_time_ms);
                    AppendField(sb, "main_thread_ms", perf.main_thread_ms);
                    AppendField(sb, "gc_alloc_kb", perf.gc_alloc_kb);
                    AppendField(sb, "mem_mb", perf.mem_mb);
                    break;

                case JankEvent jank:
                    AppendField(sb, "duration_ms", jank.duration_ms);
                    AppendField(sb, "max_frame_ms", jank.max_frame_ms);
                    AppendField(sb, "recent_gc_count", jank.recent_gc_count);
                    AppendField(sb, "recent_gc_alloc_kb", jank.recent_gc_alloc_kb);
                    AppendFieldArray(sb, "recent_events", jank.recent_events);
                    break;

                case StartupEvent startup:
                    AppendField(sb, "phase1_ms", startup.phase1_ms);
                    AppendField(sb, "phase2_ms", startup.phase2_ms);
                    AppendField(sb, "tti_ms", startup.tti_ms);
                    break;

                case SceneLoadEvent scene:
                    AppendField(sb, "scene_name", scene.scene_name);
                    AppendField(sb, "load_ms", scene.load_ms);
                    AppendField(sb, "activate_ms", scene.activate_ms);
                    break;

                case ExceptionEvent exc:
                    AppendField(sb, "fingerprint", exc.fingerprint);
                    AppendField(sb, "message", exc.message);
                    AppendField(sb, "stack", exc.stack);
                    AppendField(sb, "count", exc.count);
                    // APM-4; absent fields are what older SDKs send.
                    if (!string.IsNullOrEmpty(exc.log_type))
                        AppendField(sb, "log_type", exc.log_type);
                    if (!string.IsNullOrEmpty(exc.embedded_exception))
                        AppendField(sb, "embedded_exception", exc.embedded_exception);
                    if (exc.breadcrumbs != null && exc.breadcrumbs.Count > 0)
                        AppendFieldArray(sb, "breadcrumbs", exc.breadcrumbs);
                    break;

                case CrashEvent crash:
                    AppendField(sb, "crash_type", crash.crash_type);
                    AppendField(sb, "fingerprint", crash.fingerprint);
                    AppendField(sb, "stack", crash.stack);
                    AppendFieldArray(sb, "breadcrumbs", crash.breadcrumbs);
                    break;
            }

            // The game context, last: its JSON was built once per context change
            // and is shared by every event until the next one (APM-5).
            var ctx = evt.ctx;
            if (ctx != null && ctx.Json != null)
                sb.Append(",\"ctx\":").Append(ctx.Json);

            sb.Append('}');
        }

        private static void AppendField(StringBuilder sb, string name, string value, bool first = false)
        {
            if (!first) sb.Append(',');
            sb.Append('"').Append(name).Append("\":\"");
            AppendEscapedString(sb, value ?? "");
            sb.Append('"');
        }

        // All numbers are formatted with InvariantCulture: under e.g. de-DE the
        // default formatting writes "59,50", which is invalid JSON and gets the
        // whole batch rejected.
        private static void AppendField(StringBuilder sb, string name, long value, bool addComma = true)
        {
            if (addComma) sb.Append(',');
            sb.Append('"').Append(name).Append("\":").Append(value.ToString(CultureInfo.InvariantCulture));
        }

        private static void AppendField(StringBuilder sb, string name, int value, bool addComma = true)
        {
            if (addComma) sb.Append(',');
            sb.Append('"').Append(name).Append("\":").Append(value.ToString(CultureInfo.InvariantCulture));
        }

        private static void AppendField(StringBuilder sb, string name, float value, bool addComma = true)
        {
            if (addComma) sb.Append(',');
            sb.Append('"').Append(name).Append("\":");
            if (float.IsNaN(value) || float.IsInfinity(value))
                sb.Append("null"); // JSON has no NaN/Infinity
            else
                sb.Append(value.ToString("F2", CultureInfo.InvariantCulture));
        }

        private static void AppendFieldArray(StringBuilder sb, string name, List<string> values, bool addComma = true)
        {
            if (addComma) sb.Append(',');
            sb.Append('"').Append(name).Append("\":[");

            if (values != null)
            {
                for (int i = 0; i < values.Count; i++)
                {
                    if (i > 0) sb.Append(',');
                    sb.Append('"');
                    AppendEscapedString(sb, values[i] ?? "");
                    sb.Append('"');
                }
            }

            sb.Append(']');
        }

        internal static void AppendEscapedString(StringBuilder sb, string value)
        {
            foreach (char c in value)
            {
                switch (c)
                {
                    case '"': sb.Append("\\\""); break;
                    case '\\': sb.Append("\\\\"); break;
                    case '\n': sb.Append("\\n"); break;
                    case '\r': sb.Append("\\r"); break;
                    case '\t': sb.Append("\\t"); break;
                    default:
                        if (c < 32)
                            sb.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                        else
                            sb.Append(c);
                        break;
                }
            }
        }

        /// <summary>
        /// Gets the approximate byte size of a serialized event batch
        /// </summary>
        public static int EstimateBatchSize(List<BaseEvent> events)
        {
            // Rough estimate: 200-500 bytes per event
            return events.Count * 350;
        }
    }
}
