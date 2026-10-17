using System;

namespace OzxApm.Utils
{
    /// <summary>
    /// The bucket scheme of custom metric histograms (APM-6), version 1. The
    /// server decodes the same keys (internal/metrics/buckets.go); change both
    /// together and bump Version.
    ///
    /// Logarithmic buckets of relative width gamma = (1 + a) / (1 - a), a = 2%:
    /// bucket i holds the magnitudes in (gamma^(i-1), gamma^i] and is read back
    /// as 2 gamma^i / (gamma + 1), which is within a of every value in it
    /// (the DDSketch construction). Counts are exact and merge exactly (sum per
    /// key), so a quantile computed from the merged buckets of any number of
    /// devices and windows has the exact rank and a value within 2% of the
    /// true sample at that rank, for magnitudes in [1e-9, 4e15]. Smaller
    /// magnitudes (and 0) share key 0 (read as 0); larger ones the top bucket.
    ///
    /// Key: 0 for |v| &lt; 1e-9; otherwise sign(v) * (i + 1000) with i clamped
    /// to [-600, 900]. Keys sort in the order of the values they stand for.
    /// </summary>
    internal static class MetricBuckets
    {
        public const int Version = 1;
        public const double RelativeAccuracy = 0.02;
        public static readonly double Gamma = (1 + RelativeAccuracy) / (1 - RelativeAccuracy);
        private static readonly double s_lnGamma = Math.Log((1 + RelativeAccuracy) / (1 - RelativeAccuracy));
        public const double MinMagnitude = 1e-9;
        public const int MinIndex = -600;
        public const int MaxIndex = 900;
        public const int Offset = 1000;

        /// <summary>The key of a finite value.</summary>
        public static int Key(double v)
        {
            double a = Math.Abs(v);
            if (!(a >= MinMagnitude))
                return 0;
            double l = Math.Ceiling(Math.Log(a) / s_lnGamma);
            int i = l < MinIndex ? MinIndex : l > MaxIndex ? MaxIndex : (int)l;
            return v > 0 ? i + Offset : -(i + Offset);
        }

        /// <summary>The value a key is read back as.</summary>
        public static double Value(int key)
        {
            if (key == 0)
                return 0;
            int i = Math.Abs(key) - Offset;
            double v = 2 * Math.Pow(Gamma, i) / (Gamma + 1);
            return key > 0 ? v : -v;
        }
    }

    /// <summary>
    /// Counts per bucket key (MetricBuckets) in sorted arrays: adding a sample
    /// allocates nothing unless the arrays must grow (doubling, up to
    /// maxCapacity keys; a histogram reused across windows keeps its size).
    /// When a sample needs a new key and maxCapacity keys are in use, the two
    /// lowest keys are merged (their samples are then read as the higher one):
    /// only quantiles in the lowest part of the distribution lose accuracy,
    /// and only when one series spans more than gamma^maxCapacity (about
    /// 8e8 x at 512 keys) within one window. Not thread-safe.
    /// </summary>
    internal sealed class MetricHistogram
    {
        private int[] _keys;
        private long[] _counts;
        private int _length;
        private readonly int _maxCapacity;

        public MetricHistogram(int capacity) : this(capacity, capacity)
        {
        }

        public MetricHistogram(int initialCapacity, int maxCapacity)
        {
            _maxCapacity = Math.Max(2, maxCapacity);
            _keys = new int[Math.Max(2, Math.Min(initialCapacity, _maxCapacity))];
            _counts = new long[_keys.Length];
        }

        public int Length => _length;
        public int Capacity => _keys.Length;
        /// <summary>Lowest keys were merged since the last Clear.</summary>
        public bool Collapsed { get; private set; }

        public int KeyAt(int i) => _keys[i];
        public long CountAt(int i) => _counts[i];

        public void Clear()
        {
            _length = 0;
            Collapsed = false;
        }

        public void Add(int key)
        {
            int i = Array.BinarySearch(_keys, 0, _length, key);
            if (i >= 0)
            {
                _counts[i]++;
                return;
            }
            i = ~i;
            if (_length == _keys.Length && _keys.Length < _maxCapacity)
            {
                int size = Math.Min(_maxCapacity, _keys.Length * 2);
                Array.Resize(ref _keys, size);
                Array.Resize(ref _counts, size);
            }
            if (_length == _keys.Length)
            {
                Collapsed = true;
                if (i == 0)
                {
                    // Below every key: counted in the lowest one.
                    _counts[0]++;
                    return;
                }
                // Merge the lowest key into the next one to make room.
                _counts[1] += _counts[0];
                Array.Copy(_keys, 1, _keys, 0, _length - 1);
                Array.Copy(_counts, 1, _counts, 0, _length - 1);
                _length--;
                i--;
            }
            if (i < _length)
            {
                Array.Copy(_keys, i, _keys, i + 1, _length - i);
                Array.Copy(_counts, i, _counts, i + 1, _length - i);
            }
            _keys[i] = key;
            _counts[i] = 1;
            _length++;
        }

        /// <summary>The keys and counts (ascending keys), as new arrays.</summary>
        public void CopyTo(out int[] keys, out long[] counts)
        {
            keys = new int[_length];
            counts = new long[_length];
            Array.Copy(_keys, keys, _length);
            Array.Copy(_counts, counts, _length);
        }

        /// <summary>
        /// The value at quantile q (nearest rank) as the buckets read it back.
        /// For tests and diagnostics; the server computes the reported ones.
        /// </summary>
        public double Quantile(double q)
        {
            long total = 0;
            for (int i = 0; i < _length; i++)
                total += _counts[i];
            if (total == 0)
                return 0;
            long rank = Math.Max(1, (long)Math.Ceiling(q * total));
            long seen = 0;
            for (int i = 0; i < _length; i++)
            {
                seen += _counts[i];
                if (seen >= rank)
                    return MetricBuckets.Value(_keys[i]);
            }
            return MetricBuckets.Value(_keys[_length - 1]);
        }
    }
}
