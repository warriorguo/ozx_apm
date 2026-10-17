using System;
using System.Collections.Generic;
using System.Linq;
using NUnit.Framework;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>APM-6: the bucket scheme of custom metrics and its error bound.</summary>
    [TestFixture]
    public class MetricHistogramTests
    {
        [Test]
        public void Keys_SortLikeTheirValues_AndReadBackWithinTwoPercent()
        {
            var rnd = new Random(7);
            var values = new List<double>();
            for (int i = 0; i < 20000; i++)
            {
                double magnitude = Math.Pow(10, rnd.NextDouble() * 21 - 9); // 1e-9 .. 1e12
                values.Add(rnd.Next(4) == 0 ? -magnitude : magnitude);
            }
            values.Add(0);
            values.Sort();
            int previous = int.MinValue;
            foreach (double v in values)
            {
                int key = MetricBuckets.Key(v);
                Assert.GreaterOrEqual(key, previous, "keys are monotonic in the value");
                previous = key;
                double back = MetricBuckets.Value(key);
                if (v == 0)
                    Assert.AreEqual(0, back);
                else
                    Assert.LessOrEqual(Math.Abs(back - v) / Math.Abs(v), MetricBuckets.RelativeAccuracy + 1e-12, "value " + v);
            }
        }

        [Test]
        public void Key_Boundaries()
        {
            Assert.AreEqual(0, MetricBuckets.Key(0));
            Assert.AreEqual(0, MetricBuckets.Key(1e-10));
            Assert.AreEqual(0, MetricBuckets.Key(-1e-10));
            Assert.AreEqual(MetricBuckets.Offset, MetricBuckets.Key(1.0), "1 is the top of bucket 0");
            Assert.AreEqual(MetricBuckets.MaxIndex + MetricBuckets.Offset, MetricBuckets.Key(1e300));
            Assert.AreEqual(-(MetricBuckets.MaxIndex + MetricBuckets.Offset), MetricBuckets.Key(-1e300));
            Assert.Greater(MetricBuckets.Key(1e-9), 0);
            Assert.Less(MetricBuckets.Key(-1e-9), 0);
        }

        // The quantile read from the histogram is the value of the right rank
        // (nearest rank) within 2%: a room switch time of 200 ms - 5 s.
        [Test]
        public void Quantiles_AreWithinTheErrorBound()
        {
            var rnd = new Random(42);
            var h = new MetricHistogram(128);
            var samples = new List<double>();
            for (int i = 0; i < 5000; i++)
            {
                // Log-normal around 800 ms.
                double u1 = 1 - rnd.NextDouble(), u2 = rnd.NextDouble();
                double z = Math.Sqrt(-2 * Math.Log(u1)) * Math.Cos(2 * Math.PI * u2);
                double v = Math.Exp(Math.Log(800) + 0.5 * z);
                samples.Add(v);
                h.Add(MetricBuckets.Key(v));
            }
            Assert.IsFalse(h.Collapsed);
            samples.Sort();
            foreach (double q in new[] { 0.5, 0.9, 0.95, 0.99 })
            {
                double exact = samples[(int)Math.Ceiling(q * samples.Count) - 1];
                double est = h.Quantile(q);
                Assert.LessOrEqual(Math.Abs(est - exact) / exact, MetricBuckets.RelativeAccuracy + 1e-12, "q" + q);
            }
        }

        [Test]
        public void Full_CollapsesTheLowestKeys_UpperQuantilesStayExact()
        {
            var h = new MetricHistogram(8);
            // 16 distinct buckets, 10 samples each, ascending.
            var values = new List<double>();
            for (int b = 0; b < 16; b++)
            {
                double v = Math.Pow(MetricBuckets.Gamma, b * 3);
                for (int i = 0; i < 10; i++)
                {
                    values.Add(v);
                    h.Add(MetricBuckets.Key(v));
                }
            }
            Assert.IsTrue(h.Collapsed);
            Assert.AreEqual(8, h.Length);
            long total = 0;
            for (int i = 0; i < h.Length; i++)
                total += h.CountAt(i);
            Assert.AreEqual(160, total, "no sample is lost");
            for (int i = 1; i < h.Length; i++)
                Assert.Greater(h.KeyAt(i), h.KeyAt(i - 1));
            double p95 = h.Quantile(0.95);
            double exact = values[(int)Math.Ceiling(0.95 * values.Count) - 1];
            Assert.LessOrEqual(Math.Abs(p95 - exact) / exact, MetricBuckets.RelativeAccuracy + 1e-12);
        }

        [Test]
        public void SampleBelowAllKeys_WhenFull_IsCountedInTheLowest()
        {
            var h = new MetricHistogram(2);
            h.Add(MetricBuckets.Key(10));
            h.Add(MetricBuckets.Key(100));
            h.Add(MetricBuckets.Key(1));
            Assert.AreEqual(2, h.Length);
            Assert.AreEqual(MetricBuckets.Key(10), h.KeyAt(0));
            Assert.AreEqual(2, h.CountAt(0));
        }

        [Test]
        public void CopyTo_ReturnsAscendingKeysAndCounts()
        {
            var h = new MetricHistogram(16);
            foreach (double v in new[] { 5.0, -3, 0, 5, 1000 })
                h.Add(MetricBuckets.Key(v));
            h.CopyTo(out int[] keys, out long[] counts);
            CollectionAssert.AreEqual(keys.OrderBy(k => k).ToArray(), keys);
            Assert.AreEqual(4, keys.Length);
            Assert.AreEqual(5, counts.Sum());
            Assert.AreEqual(2, counts[Array.IndexOf(keys, MetricBuckets.Key(5))]);
            h.Clear();
            Assert.AreEqual(0, h.Length);
        }
    }
}
