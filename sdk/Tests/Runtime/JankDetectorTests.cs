using NUnit.Framework;
using OzxApm.Core;
using OzxApm.Collectors;

namespace OzxApm.Tests
{
    [TestFixture]
    public class JankDetectorTests
    {
        [Test]
        public void JankThreshold_Default()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(50f, config.JankThresholdMs);
        }

        [Test]
        public void SustainedJankThreshold_Default()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(33f, config.SustainedJankThresholdMs);
        }

        [Test]
        public void JankThreshold_Configurable()
        {
            var config = new ApmConfig { JankThresholdMs = 100f };
            Assert.AreEqual(100f, config.JankThresholdMs);
        }

        // Note: Full JankDetector testing requires Unity runtime context
        // These tests verify configuration behavior
    }
}
