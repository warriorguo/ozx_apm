using System;
using System.Collections.Generic;
using System.Globalization;
using System.Runtime.InteropServices;
using System.Text;
using OzxApm.Models;

namespace OzxApm.Utils
{
    /// <summary>
    /// Serializes events to one line of JSON each: the on-disk record and the
    /// element of the upload batch. Output does not depend on the current culture.
    ///
    /// The storage worker writes events straight into a reused UTF-8 buffer
    /// (<see cref="JsonLine"/>), so persisting an event allocates nothing once
    /// the buffer has grown to the largest line (APM-3: the once-per-second perf
    /// sample used to allocate its JSON three times over: string, byte count,
    /// bytes, about 3 KB, and 7 KB with a 30-key context). The string methods
    /// are for tests and diagnostics and produce the same text.
    /// </summary>
    public static class JsonSerializer
    {
        private static readonly object _lock = new object();
        private static readonly JsonLine _line = new JsonLine();

        /// <summary>
        /// Serializes an event batch to JSON
        /// </summary>
        public static string SerializeEventBatch(List<BaseEvent> events)
        {
            lock (_lock)
            {
                _line.Clear();
                _line.Ascii("{\"events\":[");
                for (int i = 0; i < events.Count; i++)
                {
                    if (i > 0) _line.Byte((byte)',');
                    WriteEvent(_line, events[i]);
                }
                _line.Ascii("]}");
                return _line.ToUtf8String();
            }
        }

        /// <summary>
        /// Serializes a single event to one line of JSON (no newlines inside),
        /// exactly the bytes the storage worker writes for it.
        /// </summary>
        public static string SerializeEvent(BaseEvent evt)
        {
            lock (_lock)
            {
                _line.Clear();
                WriteEvent(_line, evt);
                return _line.ToUtf8String();
            }
        }

        /// <summary>
        /// Appends the event as one line of JSON (no newline) to w. Allocates
        /// nothing unless w has to grow. Any thread, one writer per thread.
        /// </summary>
        internal static void WriteEvent(JsonLine w, BaseEvent evt)
        {
            w.Byte((byte)'{');

            // Common fields
            w.Ascii("\"type\":");
            w.String(evt.type);
            w.Name("event_id");
            if (string.IsNullOrEmpty(evt.event_id) && (evt.id_hi | evt.id_lo) != 0)
                w.Id(evt.id_hi, evt.id_lo);
            else
                w.String(evt.event_id);
            w.Name("seq");
            w.Long(evt.seq);
            w.Name("timestamp");
            w.Long(evt.timestamp);
            Field(w, "app_version", evt.app_version);
            Field(w, "platform", evt.platform);
            Field(w, "device_model", evt.device_model);
            Field(w, "os_version", evt.os_version);
            Field(w, "session_id", evt.session_id);
            Field(w, "device_id", evt.device_id);
            OptionalField(w, "scene", evt.scene);
            OptionalField(w, "build_type", evt.build_type);
            OptionalField(w, "gpu", evt.gpu);
            OptionalField(w, "ram_class", evt.ram_class);
            OptionalField(w, "net_type", evt.net_type);
            // APM-10: the game's clock (Core.GameClock), when the event has it.
            if (evt.frame >= 0)
            {
                w.Name("t");
                w.Fixed(evt.game_t, 4);
                w.Name("frame");
                w.Long(evt.frame);
            }

            // Type-specific fields
            switch (evt)
            {
                case PerfSampleEvent perf:
                    Field(w, "fps", perf.fps);
                    Field(w, "frame_time_ms", perf.frame_time_ms);
                    Field(w, "main_thread_ms", perf.main_thread_ms);
                    Field(w, "gc_alloc_kb", perf.gc_alloc_kb);
                    Field(w, "mem_mb", perf.mem_mb);
                    // APM-9; a figure that is not available (0) is not sent.
                    if (perf.interval_ms > 0)
                    {
                        w.Name("interval_ms");
                        w.Long(perf.interval_ms);
                    }
                    OptionalField(w, "mem_mono_heap_mb", perf.mem_mono_heap_mb);
                    OptionalField(w, "mem_alloc_mb", perf.mem_alloc_mb);
                    OptionalField(w, "mem_reserved_mb", perf.mem_reserved_mb);
                    OptionalField(w, "mem_gfx_mb", perf.mem_gfx_mb);
                    OptionalField(w, "mem_rss_mb", perf.mem_rss_mb);
                    break;

                case RunEvent run:
                    // APM-9
                    Field(w, "run_id", run.run_id);
                    Field(w, "end_reason", run.end_reason);
                    OptionalField(w, "result", run.result);
                    OptionalField(w, "crash_type", run.crash_type);
                    w.Name("duration_ms");
                    w.Long(run.duration_ms);
                    w.Name("samples");
                    w.Long(run.samples);
                    OptionalField(w, "peak_mem_mb", run.peak_mem_mb);
                    OptionalField(w, "peak_mono_heap_mb", run.peak_mono_heap_mb);
                    OptionalField(w, "peak_alloc_mb", run.peak_alloc_mb);
                    OptionalField(w, "peak_reserved_mb", run.peak_reserved_mb);
                    OptionalField(w, "peak_gfx_mb", run.peak_gfx_mb);
                    OptionalField(w, "peak_rss_mb", run.peak_rss_mb);
                    OptionalField(w, "start_alloc_mb", run.start_alloc_mb);
                    OptionalField(w, "start_rss_mb", run.start_rss_mb);
                    OptionalField(w, "end_alloc_mb", run.end_alloc_mb);
                    OptionalField(w, "end_rss_mb", run.end_rss_mb);
                    break;

                case JankEvent jank:
                    Field(w, "duration_ms", jank.duration_ms);
                    Field(w, "max_frame_ms", jank.max_frame_ms);
                    w.Name("recent_gc_count");
                    w.Long(jank.recent_gc_count);
                    Field(w, "recent_gc_alloc_kb", jank.recent_gc_alloc_kb);
                    ArrayField(w, "recent_events", jank.recent_events);
                    break;

                case StartupEvent startup:
                    Field(w, "phase1_ms", startup.phase1_ms);
                    Field(w, "phase2_ms", startup.phase2_ms);
                    Field(w, "tti_ms", startup.tti_ms);
                    break;

                case SceneLoadEvent scene:
                    Field(w, "scene_name", scene.scene_name);
                    Field(w, "load_ms", scene.load_ms);
                    Field(w, "activate_ms", scene.activate_ms);
                    break;

                case ExceptionEvent exc:
                    Field(w, "fingerprint", exc.fingerprint);
                    Field(w, "message", exc.message);
                    Field(w, "stack", exc.stack);
                    w.Name("count");
                    w.Long(exc.count);
                    // APM-4; absent fields are what older SDKs send.
                    OptionalField(w, "log_type", exc.log_type);
                    OptionalField(w, "embedded_exception", exc.embedded_exception);
                    if (exc.breadcrumbs != null && exc.breadcrumbs.Count > 0)
                        ArrayField(w, "breadcrumbs", exc.breadcrumbs);
                    // APM-3; absent from older SDKs.
                    if (exc.error_seq > 0)
                    {
                        w.Name("error_seq");
                        w.Long(exc.error_seq);
                        w.Name("session_ms");
                        w.Long(exc.session_ms);
                    }
                    OptionalField(w, "unhandled", exc.unhandled);
                    if (exc.ctx_merged)
                        w.Ascii(",\"ctx_merged\":true");
                    break;

                case CrashEvent crash:
                    Field(w, "crash_type", crash.crash_type);
                    Field(w, "fingerprint", crash.fingerprint);
                    Field(w, "stack", crash.stack);
                    ArrayField(w, "breadcrumbs", crash.breadcrumbs);
                    // APM-7; absent fields are what an older server ignores.
                    OptionalField(w, "exit_reason", crash.exit_reason);
                    if (crash.exit_status != 0)
                    {
                        w.Name("exit_status");
                        w.Long(crash.exit_status);
                    }
                    OptionalField(w, "exit_description", crash.exit_description);
                    if (crash.importance != 0)
                    {
                        w.Name("importance");
                        w.Long(crash.importance);
                    }
                    if (crash.pss_kb > 0)
                    {
                        w.Name("pss_kb");
                        w.Long(crash.pss_kb);
                    }
                    if (crash.rss_kb > 0)
                    {
                        w.Name("rss_kb");
                        w.Long(crash.rss_kb);
                    }
                    OptionalField(w, "signal", crash.signal);
                    OptionalField(w, "signal_code", crash.signal_code);
                    OptionalField(w, "fault_addr", crash.fault_addr);
                    OptionalField(w, "abort_message", crash.abort_message);
                    OptionalField(w, "abi", crash.abi);
                    if (crash.session_ms > 0)
                    {
                        w.Name("session_ms");
                        w.Long(crash.session_ms);
                    }
                    if (crash.foreground)
                        w.Ascii(",\"foreground\":true");
                    var details = crash.details;
                    if (details != null && details.Length >= 2)
                    {
                        w.Name("details");
                        w.Byte((byte)'{');
                        for (int i = 0; i + 1 < details.Length; i += 2)
                        {
                            if (i > 0) w.Byte((byte)',');
                            w.String(details[i]);
                            w.Byte((byte)':');
                            w.String(details[i + 1]);
                        }
                        w.Byte((byte)'}');
                    }
                    break;

                // APM-6
                case CustomEvent ce:
                    Field(w, "name", ce.name);
                    w.Name("count");
                    w.Long(ce.count);
                    if (ce.aggregated)
                        w.Ascii(",\"aggregated\":true");
                    if (ce.ctx_merged)
                        w.Ascii(",\"ctx_merged\":true");
                    var props = ce.props;
                    if (props != null && props.Length >= 2)
                    {
                        var kinds = ce.prop_kinds;
                        w.Name("props");
                        w.Byte((byte)'{');
                        for (int i = 0; i + 1 < props.Length; i += 2)
                        {
                            if (i > 0) w.Byte((byte)',');
                            w.String(props[i]);
                            w.Byte((byte)':');
                            // APM-10: numbers and booleans unquoted (their text
                            // was written by the SDK); anything else a string.
                            string v = props[i + 1];
                            if (kinds != null && i / 2 < kinds.Length && IsJsonLiteral(kinds[i / 2], v))
                                w.Ascii(v);
                            else
                                w.String(v);
                        }
                        w.Byte((byte)'}');
                    }
                    break;

                case CustomMetricEvent m:
                    Field(w, "name", m.name);
                    OptionalField(w, "unit", m.unit);
                    w.Name("count");
                    w.Long(m.count);
                    w.Name("sum");
                    w.Double(m.sum);
                    w.Name("min");
                    w.Double(m.min);
                    w.Name("max");
                    w.Double(m.max);
                    w.Name("window_ms");
                    w.Long(m.window_ms);
                    w.Name("hist_v");
                    w.Long(m.hist_v);
                    w.Name("hist_keys");
                    w.Byte((byte)'[');
                    if (m.hist_keys != null)
                    {
                        for (int i = 0; i < m.hist_keys.Length; i++)
                        {
                            if (i > 0) w.Byte((byte)',');
                            w.Long(m.hist_keys[i]);
                        }
                    }
                    w.Byte((byte)']');
                    w.Name("hist_counts");
                    w.Byte((byte)'[');
                    if (m.hist_counts != null)
                    {
                        for (int i = 0; i < m.hist_counts.Length; i++)
                        {
                            if (i > 0) w.Byte((byte)',');
                            w.Long(m.hist_counts[i]);
                        }
                    }
                    w.Byte((byte)']');
                    if (m.ctx_merged)
                        w.Ascii(",\"ctx_merged\":true");
                    break;
            }

            // The game context, last: its UTF-8 JSON was built once per context
            // change and is shared by every event until the next one (APM-5).
            var ctx = evt.ctx;
            if (ctx != null && ctx.JsonUtf8 != null)
            {
                w.Ascii(",\"ctx\":");
                w.Bytes(ctx.JsonUtf8);
            }

            w.Byte((byte)'}');
        }

        /// <summary>
        /// True if v may be written unquoted as a value of the given kind:
        /// "true" or "false" for Bool; for Number, JSON's number grammar (an
        /// optional '-', an integer part, an optional fraction and exponent).
        /// Never allocates.
        /// </summary>
        internal static bool IsJsonLiteral(byte kind, string v)
        {
            if (string.IsNullOrEmpty(v))
                return false;
            if (kind == PropKinds.Bool)
                return v == "true" || v == "false";
            if (kind != PropKinds.Number)
                return false;
            int i = 0, n = v.Length;
            if (v[i] == '-' && ++i == n)
                return false;
            if (v[i] == '0')
            {
                i++;
            }
            else if (v[i] >= '1' && v[i] <= '9')
            {
                while (i < n && v[i] >= '0' && v[i] <= '9') i++;
            }
            else
            {
                return false;
            }
            if (i < n && v[i] == '.')
            {
                int start = ++i;
                while (i < n && v[i] >= '0' && v[i] <= '9') i++;
                if (i == start) return false;
            }
            if (i < n && (v[i] == 'e' || v[i] == 'E'))
            {
                i++;
                if (i < n && (v[i] == '+' || v[i] == '-')) i++;
                int start = i;
                while (i < n && v[i] >= '0' && v[i] <= '9') i++;
                if (i == start) return false;
            }
            return i == n;
        }

        private static void Field(JsonLine w, string name, string value)
        {
            w.Name(name);
            w.String(value);
        }

        private static void OptionalField(JsonLine w, string name, string value)
        {
            if (string.IsNullOrEmpty(value))
                return;
            w.Name(name);
            w.String(value);
        }

        private static void Field(JsonLine w, string name, float value)
        {
            w.Name(name);
            w.Float2(value);
        }

        // A figure that is 0 (or not a positive finite number) is not available: not sent.
        private static void OptionalField(JsonLine w, string name, float value)
        {
            if (!(value > 0) || float.IsInfinity(value))
                return;
            w.Name(name);
            w.Float2(value);
        }

        private static void ArrayField(JsonLine w, string name, List<string> values)
        {
            w.Name(name);
            w.Byte((byte)'[');
            if (values != null)
            {
                for (int i = 0; i < values.Count; i++)
                {
                    if (i > 0) w.Byte((byte)',');
                    w.String(values[i]);
                }
            }
            w.Byte((byte)']');
        }

        /// <summary>
        /// Appends value to sb as the body of a JSON string (no quotes): the
        /// same escaping as <see cref="JsonLine.String"/>.
        /// </summary>
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

    /// <summary>
    /// A reusable buffer holding one line of JSON as UTF-8. Writing allocates
    /// only when the buffer has to grow. Not thread-safe.
    ///
    /// Strings are escaped as before APM-3 (quote, backslash, \n, \r, \t,
    /// other control characters as \u00xx) and encoded like Encoding.UTF8
    /// (a lone surrogate becomes U+FFFD). Floats are written like
    /// float.ToString("F2", CultureInfo.InvariantCulture) on Unity's Mono and
    /// IL2CPP class libraries, digit for digit (checked against it for every
    /// float below 0.03125 and every 13th one up to 1e15).
    /// </summary>
    internal sealed class JsonLine
    {
        private const int InitialCapacity = 4096;
        // A buffer grown beyond this (a huge exception) is dropped on Clear,
        // so one big line does not stay resident.
        private const int MaxRetainedCapacity = 64 * 1024;

        private byte[] _buf;
        private int _len;

        public JsonLine(int capacity = InitialCapacity)
        {
            _buf = new byte[Math.Max(16, capacity)];
        }

        public byte[] Buffer => _buf;
        public int Length => _len;

        public void Clear()
        {
            _len = 0;
            if (_buf.Length > MaxRetainedCapacity)
                _buf = new byte[InitialCapacity];
        }

        public string ToUtf8String() => Encoding.UTF8.GetString(_buf, 0, _len);

        private void Ensure(int extra)
        {
            if (_len + extra <= _buf.Length)
                return;
            int size = Math.Max(_buf.Length * 2, _len + extra);
            var bigger = new byte[size];
            System.Buffer.BlockCopy(_buf, 0, bigger, 0, _len);
            _buf = bigger;
        }

        public void Byte(byte b)
        {
            if (_len == _buf.Length)
                Ensure(1);
            _buf[_len++] = b;
        }

        public void Bytes(byte[] bytes)
        {
            Ensure(bytes.Length);
            System.Buffer.BlockCopy(bytes, 0, _buf, _len, bytes.Length);
            _len += bytes.Length;
        }

        /// <summary>Writes s, which must be ASCII (field names, literals), as is.</summary>
        public void Ascii(string s)
        {
            Ensure(s.Length);
            for (int i = 0; i < s.Length; i++)
                _buf[_len++] = (byte)s[i];
        }

        /// <summary>Writes ,"name": (name is ASCII and needs no escaping).</summary>
        public void Name(string name)
        {
            Ensure(name.Length + 4);
            _buf[_len++] = (byte)',';
            _buf[_len++] = (byte)'"';
            for (int i = 0; i < name.Length; i++)
                _buf[_len++] = (byte)name[i];
            _buf[_len++] = (byte)'"';
            _buf[_len++] = (byte)':';
        }

        /// <summary>Writes a quoted, escaped JSON string; null is written as "".</summary>
        public void String(string s)
        {
            Byte((byte)'"');
            if (s != null)
            {
                // At most 6 bytes per UTF-16 unit (\u00xx); reserve in chunks.
                int n = s.Length;
                for (int i = 0; i < n; i++)
                {
                    if (_buf.Length - _len < 8)
                        Ensure(Math.Max(8, (n - i) * 3 + 8));
                    char c = s[i];
                    if (c < 0x80)
                    {
                        switch (c)
                        {
                            case '"': _buf[_len++] = (byte)'\\'; _buf[_len++] = (byte)'"'; break;
                            case '\\': _buf[_len++] = (byte)'\\'; _buf[_len++] = (byte)'\\'; break;
                            case '\n': _buf[_len++] = (byte)'\\'; _buf[_len++] = (byte)'n'; break;
                            case '\r': _buf[_len++] = (byte)'\\'; _buf[_len++] = (byte)'r'; break;
                            case '\t': _buf[_len++] = (byte)'\\'; _buf[_len++] = (byte)'t'; break;
                            default:
                                if (c < 32)
                                {
                                    _buf[_len++] = (byte)'\\';
                                    _buf[_len++] = (byte)'u';
                                    _buf[_len++] = (byte)'0';
                                    _buf[_len++] = (byte)'0';
                                    _buf[_len++] = HexDigit(c >> 4);
                                    _buf[_len++] = HexDigit(c & 0xF);
                                }
                                else
                                {
                                    _buf[_len++] = (byte)c;
                                }
                                break;
                        }
                    }
                    else if (c < 0x800)
                    {
                        _buf[_len++] = (byte)(0xC0 | (c >> 6));
                        _buf[_len++] = (byte)(0x80 | (c & 0x3F));
                    }
                    else if (char.IsHighSurrogate(c) && i + 1 < n && char.IsLowSurrogate(s[i + 1]))
                    {
                        int cp = char.ConvertToUtf32(c, s[i + 1]);
                        i++;
                        _buf[_len++] = (byte)(0xF0 | (cp >> 18));
                        _buf[_len++] = (byte)(0x80 | ((cp >> 12) & 0x3F));
                        _buf[_len++] = (byte)(0x80 | ((cp >> 6) & 0x3F));
                        _buf[_len++] = (byte)(0x80 | (cp & 0x3F));
                    }
                    else if (char.IsSurrogate(c))
                    {
                        // A lone surrogate: U+FFFD, as Encoding.UTF8 writes it.
                        _buf[_len++] = 0xEF;
                        _buf[_len++] = 0xBF;
                        _buf[_len++] = 0xBD;
                    }
                    else
                    {
                        _buf[_len++] = (byte)(0xE0 | (c >> 12));
                        _buf[_len++] = (byte)(0x80 | ((c >> 6) & 0x3F));
                        _buf[_len++] = (byte)(0x80 | (c & 0x3F));
                    }
                }
            }
            Byte((byte)'"');
        }

        private static byte HexDigit(int v) => (byte)(v < 10 ? '0' + v : 'a' + v - 10);

        /// <summary>Writes a quoted 32-digit lowercase hex id (as Guid.ToString("N")).</summary>
        public void Id(ulong hi, ulong lo)
        {
            Ensure(34);
            _buf[_len++] = (byte)'"';
            for (int shift = 60; shift >= 0; shift -= 4)
                _buf[_len++] = HexDigit((int)((hi >> shift) & 0xF));
            for (int shift = 60; shift >= 0; shift -= 4)
                _buf[_len++] = HexDigit((int)((lo >> shift) & 0xF));
            _buf[_len++] = (byte)'"';
        }

        /// <summary>Writes an integer in invariant form.</summary>
        public void Long(long value)
        {
            Ensure(20);
            ulong u;
            if (value < 0)
            {
                _buf[_len++] = (byte)'-';
                u = (ulong)(-(value + 1)) + 1; // also for long.MinValue
            }
            else
            {
                u = (ulong)value;
            }
            int start = _len;
            do
            {
                _buf[_len++] = (byte)('0' + (int)(u % 10));
                u /= 10;
            } while (u != 0);
            Array.Reverse(_buf, start, _len - start);
        }

        /// <summary>
        /// Writes a double so that it parses back to the same value: integers
        /// below 1e15 as such (no allocation), others in round-trip form
        /// (allocates a short string; custom metrics only, once per series and
        /// window). NaN and infinities are written as null.
        /// </summary>
        public void Double(double value)
        {
            if (double.IsNaN(value) || double.IsInfinity(value))
            {
                Ascii("null");
                return;
            }
            if (Math.Abs(value) < 1e15 && value == Math.Floor(value))
            {
                Long((long)value);
                return;
            }
            Ascii(value.ToString("R", CultureInfo.InvariantCulture));
        }

        private static readonly double[] s_pow10d = { 1, 10, 100, 1000, 10000, 100000, 1000000 };
        private static readonly long[] s_pow10l = { 1, 10, 100, 1000, 10000, 100000, 1000000 };

        /// <summary>
        /// Writes value with a fixed number of decimals (0-6), rounded half away
        /// from zero, as AiTelemetry's lines are written ("F4" for t, "F3" for
        /// numbers): no allocation while |value| x 10^decimals is below 9e15,
        /// the round-trip form beyond (allocates). NaN and infinities are
        /// written as null.
        /// </summary>
        public void Fixed(double value, int decimals)
        {
            if (double.IsNaN(value) || double.IsInfinity(value))
            {
                Ascii("null");
                return;
            }
            if (!FixedText(value, decimals, out long units, out int d, out bool negative))
            {
                Ascii(value.ToString("R", CultureInfo.InvariantCulture));
                return;
            }
            Ensure(24);
            if (negative)
                _buf[_len++] = (byte)'-';
            long pow = s_pow10l[d];
            Long(units / pow);
            if (d == 0)
                return;
            Ensure(8);
            _buf[_len++] = (byte)'.';
            long frac = units % pow;
            for (long p = pow / 10; p >= 1; p /= 10)
            {
                _buf[_len++] = (byte)('0' + (int)(frac / p));
                frac %= p;
            }
        }

        // The fixed-point form of a finite value: units of 10^-d, and the sign
        // to write. False (d = -1) when it does not fit a long exactly.
        private static bool FixedText(double value, int decimals, out long units, out int d, out bool negative)
        {
            d = decimals < 0 ? 0 : decimals > 6 ? 6 : decimals;
            double scaled = Math.Abs(value) * s_pow10d[d];
            if (scaled >= 9e15)
            {
                units = 0;
                negative = false;
                d = -1;
                return false;
            }
            units = (long)Math.Round(scaled, MidpointRounding.AwayFromZero);
            negative = value < 0 && units != 0;
            return true;
        }

        /// <summary>The text Fixed writes (allocates; for property values and tests).</summary>
        internal static string FixedString(double value, int decimals)
        {
            if (double.IsNaN(value) || double.IsInfinity(value))
                return "null";
            if (!FixedText(value, decimals, out long units, out int d, out bool negative))
                return value.ToString("R", CultureInfo.InvariantCulture);
            // One allocation: the string. At most 1 + 16 + 1 + 6 characters.
            Span<char> buf = stackalloc char[32];
            int n = 0;
            if (negative)
                buf[n++] = '-';
            long pow = s_pow10l[d];
            long whole = units / pow;
            int start = n;
            do
            {
                buf[n++] = (char)('0' + (int)(whole % 10));
                whole /= 10;
            } while (whole != 0);
            buf.Slice(start, n - start).Reverse();
            if (d > 0)
            {
                buf[n++] = '.';
                long frac = units % pow;
                for (long p = pow / 10; p >= 1; p /= 10)
                {
                    buf[n++] = (char)('0' + (int)(frac / p));
                    frac %= p;
                }
            }
            return new string(buf.Slice(0, n));
        }

        [StructLayout(LayoutKind.Explicit)]
        private struct FloatBits
        {
            [FieldOffset(0)] public float Float;
            [FieldOffset(0)] public int Int;
        }

        private static readonly decimal[] s_pow10 =
        {
            1m, 10m, 100m, 1000m, 10000m, 100000m, 1000000m, 10000000m, 100000000m, 1000000000m,
            10000000000m, 100000000000m, 1000000000000m, 10000000000000m, 100000000000000m, 1000000000000000m
        };

        /// <summary>
        /// Writes value with two decimals, as float.ToString("F2", InvariantCulture)
        /// does on Unity's class libraries: the exact value is rounded to 7
        /// significant digits (half away from zero), then to 2 decimals; no
        /// sign on zero. NaN and infinities are written as null (JSON has none).
        /// </summary>
        public void Float2(float value)
        {
            if (float.IsNaN(value) || float.IsInfinity(value))
            {
                Ascii("null");
                return;
            }
            float a = Math.Abs(value);
            if (a >= 1e15f)
            {
                // Never a metric; allocates.
                Ascii(value.ToString("F2", CultureInfo.InvariantCulture));
                return;
            }

            long hundredths;
            if (a < 0.03125f)
            {
                // Below 2^-5 the exact value needs more than 28 decimals; only
                // these 7-digit ties decide between 0.00 and 0.03.
                double x = a;
                hundredths = x >= 0.024999995 ? 3 : x >= 0.014999995 ? 2 : x >= 0.0049999995 ? 1 : 0;
            }
            else
            {
                decimal d = Math.Round(RoundSignificant7(ExactDecimal(a)), 2, MidpointRounding.AwayFromZero);
                hundredths = (long)(d * 100m);
            }

            Ensure(24);
            if (value < 0 && hundredths != 0)
                _buf[_len++] = (byte)'-';
            Long(hundredths / 100);
            _buf[_len++] = (byte)'.';
            int frac = (int)(hundredths % 100);
            _buf[_len++] = (byte)('0' + frac / 10);
            _buf[_len++] = (byte)('0' + frac % 10);
        }

        // The exact value of a float in [2^-5, 1e15): m * 2^e fits a decimal
        // (at most 28 decimals, m * 5^28 < 2^96).
        private static decimal ExactDecimal(float a)
        {
            int bits = new FloatBits { Float = a }.Int;
            int e = ((bits >> 23) & 0xFF) - 150;
            long m = (bits & 0x7FFFFF) | 0x800000;
            if (e >= 0)
                return m * (decimal)(1L << e);
            return m / (decimal)(1L << -e);
        }

        private static decimal RoundSignificant7(decimal d)
        {
            if (d >= 1m)
            {
                int intDigits = 1;
                while (intDigits < s_pow10.Length && d >= s_pow10[intDigits])
                    intDigits++;
                int decimals = 7 - intDigits;
                if (decimals >= 0)
                    return Math.Round(d, decimals, MidpointRounding.AwayFromZero);
                decimal scale = s_pow10[-decimals];
                return Math.Round(d / scale, 0, MidpointRounding.AwayFromZero) * scale;
            }
            int zeros = 0; // after the decimal point, before the first digit
            while (d * s_pow10[zeros + 1] < 1m)
                zeros++;
            return Math.Round(d, 7 + zeros, MidpointRounding.AwayFromZero);
        }
    }
}
