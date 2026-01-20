using NUnit.Framework;
using OzxApm.Core;

namespace OzxApm.Tests.Editor
{
    [TestFixture]
    public class ConfigValidationTests
    {
        [Test]
        public void DefaultConfig_HasValidDefaults()
        {
            var config = ApmConfig.Default();

            Assert.IsNotNull(config);
            Assert.IsTrue(config.Enabled);
            Assert.IsTrue(config.BatchSize > 0);
            Assert.IsTrue(config.MaxQueueSize > 0);
            Assert.IsTrue(config.SamplingIntervalSeconds > 0);
            Assert.IsTrue(config.FlushIntervalSeconds > 0);
            Assert.IsTrue(config.RequestTimeoutSeconds > 0);
        }

        [Test]
        public void MinimalConfig_ReducesOverhead()
        {
            var config = ApmConfig.Minimal();

            Assert.IsFalse(config.EnablePerformance);
            Assert.IsFalse(config.EnableJankDetection);
            Assert.IsFalse(config.EnableStartupTiming);
            Assert.IsFalse(config.EnableSceneLoadTracking);
            Assert.GreaterOrEqual(config.SamplingIntervalSeconds, 5.0f);
            Assert.GreaterOrEqual(config.FlushIntervalSeconds, 60.0f);
        }

        [Test]
        public void DefaultConfig_EnablesAllFeatures()
        {
            var config = ApmConfig.Default();

            Assert.IsTrue(config.EnablePerformance);
            Assert.IsTrue(config.EnableJankDetection);
            Assert.IsTrue(config.EnableExceptionCapture);
            Assert.IsTrue(config.EnableStartupTiming);
            Assert.IsTrue(config.EnableSceneLoadTracking);
        }

        [Test]
        public void Config_ServerUrlDefault()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual("http://localhost:8080", config.ServerUrl);
        }

        [Test]
        public void Config_BatchSizeDefault()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(20, config.BatchSize);
        }

        [Test]
        public void Config_MaxBatchBytesDefault()
        {
            var config = ApmConfig.Default();
            Assert.AreEqual(64 * 1024, config.MaxBatchBytes);
        }

        [Test]
        public void Config_OfflineStorageEnabled()
        {
            var config = ApmConfig.Default();
            Assert.IsTrue(config.EnableOfflineStorage);
            Assert.AreEqual(5 * 1024 * 1024, config.MaxOfflineStorageBytes);
        }

        [Test]
        public void Config_CompressionEnabled()
        {
            var config = ApmConfig.Default();
            Assert.IsTrue(config.EnableCompression);
        }
    }
}
