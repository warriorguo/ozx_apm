using System;
using System.IO;
using NUnit.Framework;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Network;

namespace OzxApm.Tests
{
    [TestFixture]
    public class NetworkLoggerTests
    {
        private string _path;

        [SetUp]
        public void SetUp()
        {
            _path = Path.Combine(Path.GetTempPath(), "ozx_apm_netlog_" + Guid.NewGuid().ToString("N") + ".log");
        }

        [TearDown]
        public void TearDown()
        {
            if (File.Exists(_path)) File.Delete(_path);
            if (File.Exists(_path + ".1")) File.Delete(_path + ".1");
        }

        [Test]
        public void ReleaseBuild_DoesNothing()
        {
            var logger = new NetworkLogger(new ApmConfig { LogLevel = LogLevel.Warning }, _path, false);
            logger.LogRequest("u", "POST", null, "body", 4, false);
            logger.LogFailure("u", 500, "e", "b", 1, 1, 1, 1f);

            Assert.IsFalse(logger.IsEnabled);
            Assert.AreEqual(0, logger.GetRecentLogs().Count);
            Assert.IsFalse(File.Exists(_path));
        }

        [Test]
        public void LogFile_IsCapped()
        {
            var logger = new NetworkLogger(new ApmConfig { LogLevel = LogLevel.Warning }, _path, true);
            string body = new string('x', 900);
            for (int i = 0; i < 3000; i++)
                logger.LogRequest("http://x/v1/events", "POST", null, body, body.Length, false);

            Assert.LessOrEqual(new FileInfo(_path).Length, NetworkLogger.MaxLogFileBytes);
            Assert.LessOrEqual(new FileInfo(_path + ".1").Length, NetworkLogger.MaxLogFileBytes);
        }

        [Test]
        public void NoFileWithoutConsent()
        {
            var logger = new NetworkLogger(new ApmConfig { Consent = ConsentState.Unknown }, _path, true);
            logger.LogRequest("u", "POST", null, "body", 4, false);
            Assert.IsFalse(File.Exists(_path));
        }
    }
}
