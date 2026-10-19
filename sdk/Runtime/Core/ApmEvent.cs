using System;
using System.Collections.Generic;
using System.Globalization;
using OzxApm.Collectors;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>
    /// A custom event with typed properties (APM-10), built like an AiTelemetry
    /// line and sent with <see cref="Send"/>:
    /// <code>
    /// ApmClient.Event("playermovement.damaged")
    ///     .Num("amount", amount).Str("srcKind", kind).Int("srcInstanceId", id)
    ///     .Bool("isCrit", crit).Vec("pos", pos.x, pos.y).Str("roomId", roomId)
    ///     .Send();
    /// </code>
    /// Numbers and booleans are sent as JSON numbers and booleans (stored as
    /// text like every property, plus their type), a vector as two numbers
    /// "key.x" and "key.y" (the session export turns them back into
    /// {"x":..,"y":..}). Numbers are written with a fixed number of decimals
    /// (3 by default, as AiTelemetry's Num). Everything else is
    /// <see cref="ApmClient.TrackEvent(string)"/>: the name and key rules, the
    /// limits (64 properties, values up to 256 characters, about 4 KB), the
    /// per-minute budget, the breadcrumb time line, the game context, and the
    /// game clock (t, frame) of the Send call.
    ///
    /// A value: copying it costs nothing. The properties are collected in a
    /// buffer of the calling thread, so build and send an event on one thread,
    /// one at a time: starting another event on the same thread before Send
    /// abandons the first (its Send returns false). When the SDK is not
    /// collecting (before Initialize, no technical consent, custom events
    /// disabled) every call does nothing, without allocating.
    ///
    /// Cost of Send: the event (as TrackEvent), one array of the key/value
    /// strings and one of their types, and one string per number.
    /// </summary>
    public readonly struct ApmEvent
    {
        private readonly EventBuilder _builder;
        private readonly int _token;

        internal ApmEvent(EventBuilder builder, int token)
        {
            _builder = builder;
            _token = token;
        }

        /// <summary>False when nothing will be recorded (see the type's comment), or after Send.</summary>
        public bool IsActive => _builder != null && _builder.Token == _token;

        /// <summary>A string property (null or empty: left out).</summary>
        public ApmEvent Str(string key, string value)
        {
            if (IsActive)
                _builder.Add(key, value, PropKinds.String);
            return this;
        }

        /// <summary>An integer property, sent as a JSON number.</summary>
        public ApmEvent Int(string key, long value)
        {
            if (IsActive)
                _builder.Add(key, value.ToString(CultureInfo.InvariantCulture), PropKinds.Number);
            return this;
        }

        /// <summary>
        /// A number, sent as a JSON number with <paramref name="decimals"/>
        /// fixed decimals (0-6; AiTelemetry writes 3). NaN and infinities are
        /// left out (a warning, counted as refused).
        /// </summary>
        public ApmEvent Num(string key, double value, int decimals = 3)
        {
            if (IsActive)
            {
                if (double.IsNaN(value) || double.IsInfinity(value))
                    CustomCollector.ReportViolation("Event", key, "number is NaN or infinite; left out");
                else
                    _builder.Add(key, JsonLine.FixedString(value, decimals), PropKinds.Number);
            }
            return this;
        }

        /// <summary>A boolean property, sent as JSON true or false.</summary>
        public ApmEvent Bool(string key, bool value)
        {
            if (IsActive)
                _builder.Add(key, value ? "true" : "false", PropKinds.Bool);
            return this;
        }

        /// <summary>
        /// A 2D vector (a position, a direction): the numbers "key.x" and
        /// "key.y", 3 decimals; the export writes {"x":..,"y":..} as
        /// AiTelemetry's Vec does. The key must leave room for ".x" (62
        /// characters at most).
        /// </summary>
        public ApmEvent Vec(string key, float x, float y)
        {
            if (IsActive)
            {
                if (float.IsNaN(x) || float.IsInfinity(x) || float.IsNaN(y) || float.IsInfinity(y))
                {
                    CustomCollector.ReportViolation("Event", key, "vector is NaN or infinite; left out");
                    return this;
                }
                _builder.VecKeys(key, out string kx, out string ky);
                _builder.Add(kx, JsonLine.FixedString(x, 3), PropKinds.Number);
                _builder.Add(ky, JsonLine.FixedString(y, 3), PropKinds.Number);
            }
            return this;
        }

        /// <summary>
        /// Records the event (see ApmClient.TrackEvent). False if nothing was
        /// recorded: not collecting, an invalid name, sent already, or another
        /// event was started on this thread since.
        /// </summary>
        public bool Send()
        {
            if (!IsActive)
                return false;
            try
            {
                return _builder.Send();
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("Event.Send", ex);
                return false;
            }
        }
    }

    /// <summary>
    /// The properties of the ApmEvent being built on one thread (one instance
    /// per thread, reused).
    /// </summary>
    internal sealed class EventBuilder
    {
        // Room for every property the limits allow, plus the ones that will be
        // refused (counted by ValidateProps when sent).
        private const int Capacity = 2 * CustomCollector.MaxProps;
        private const int MaxVecKeys = 64;

        [ThreadStatic] private static EventBuilder t_builder;

        internal int Token;
        private string _name;
        private CustomCollector _collector;
        private readonly string[] _pairs = new string[2 * Capacity];
        private readonly byte[] _kinds = new byte[Capacity];
        private int _count; // pairs
        private bool _overflowReported;
        private readonly Dictionary<string, string[]> _vecKeys = new Dictionary<string, string[]>(StringComparer.Ordinal);

        /// <summary>Starts an event on this thread (abandons one being built).</summary>
        internal static ApmEvent Begin(CustomCollector collector, string name)
        {
            var b = t_builder ?? (t_builder = new EventBuilder());
            b.Token++;
            b.Clear();
            b._name = name;
            b._collector = collector;
            return new ApmEvent(b, b.Token);
        }

        private void Clear()
        {
            for (int i = 0; i < 2 * _count; i++)
                _pairs[i] = null;
            _count = 0;
            _overflowReported = false;
        }

        internal void Add(string key, string value, byte kind)
        {
            if (_count >= Capacity)
            {
                if (!_overflowReported)
                {
                    _overflowReported = true;
                    CustomCollector.ReportViolation("Event", key, "more than 64 properties; left out");
                }
                return;
            }
            _pairs[2 * _count] = key;
            _pairs[2 * _count + 1] = value;
            _kinds[_count] = kind;
            _count++;
        }

        internal void VecKeys(string key, out string kx, out string ky)
        {
            if (key == null)
            {
                kx = ky = null;
                return;
            }
            if (!_vecKeys.TryGetValue(key, out var keys))
            {
                if (_vecKeys.Count >= MaxVecKeys)
                    _vecKeys.Clear();
                keys = new[] { key + ".x", key + ".y" };
                _vecKeys[key] = keys;
            }
            kx = keys[0];
            ky = keys[1];
        }

        internal bool Send()
        {
            Token++; // sent: the ApmEvent values of it are inactive from here on
            var collector = _collector;
            string name = _name;
            int n = _count;
            string[] pairs = null;
            byte[] kinds = null;
            if (n > 0)
            {
                pairs = new string[2 * n];
                Array.Copy(_pairs, pairs, 2 * n);
                kinds = new byte[n];
                Array.Copy(_kinds, kinds, n);
            }
            Clear();
            _collector = null;
            _name = null;
            if (collector == null || !collector.IsActive)
                return false;
            var kept = CustomCollector.ValidateProps(pairs, 2 * n, kinds, out byte[] keptKinds);
            return collector.TrackEvent(name, kept, keptKinds);
        }
    }
}
