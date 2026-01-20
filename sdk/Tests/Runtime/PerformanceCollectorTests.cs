using NUnit.Framework;
using OzxApm.Core;

namespace OzxApm.Tests
{
    [TestFixture]
    public class PerformanceCollectorTests
    {
        [Test]
        public void SamplingInterval_Default()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(1.0f, config.SamplingIntervalSeconds);
        }

        [Test]
        public void SamplingInterval_Configurable()
        {
            var config = new ApmConfig { SamplingIntervalSeconds = 5.0f };
            Assert.AreEqual(5.0f, config.SamplingIntervalSeconds);
        }

        [Test]
        public void EnablePerformance_DefaultTrue()
        {
            var config = ApmConfig.Default();
            Assert.IsTrue(config.EnablePerformance);
        }

        [Test]
        public void MinimalConfig_DisablesPerformance()
        {
            var config = ApmConfig.Minimal();
            Assert.IsFalse(config.EnablePerformance);
        }

        // Note: Full PerformanceCollector testing requires Unity runtime context
        // These tests verify configuration behavior
    }
}
