using NUnit.Framework;
using OzxApm.Core;

namespace OzxApm.Tests
{
    [TestFixture]
    public class SessionManagerTests
    {
        private SessionManager _manager;

        [SetUp]
        public void SetUp()
        {
            _manager = new SessionManager();
        }

        [Test]
        public void Constructor_GeneratesDeviceId()
        {
            Assert.IsFalse(string.IsNullOrEmpty(_manager.DeviceId));
            Assert.AreEqual(32, _manager.DeviceId.Length); // SHA256 truncated to 32 chars
        }

        [Test]
        public void Constructor_GeneratesSessionId()
        {
            Assert.IsFalse(string.IsNullOrEmpty(_manager.SessionId));
            Assert.IsTrue(_manager.SessionId.Contains("_")); // Format: timestamp_random
        }

        [Test]
        public void SetUserId_SetsUserId()
        {
            _manager.SetUserId("user123");
            Assert.AreEqual("user123", _manager.UserId);
        }

        [Test]
        public void ClearUserId_ClearsUserId()
        {
            _manager.SetUserId("user123");
            _manager.ClearUserId();
            Assert.IsNull(_manager.UserId);
        }

        [Test]
        public void StartNewSession_GeneratesNewSessionId()
        {
            string originalSessionId = _manager.SessionId;
            _manager.StartNewSession();
            Assert.AreNotEqual(originalSessionId, _manager.SessionId);
        }

        [Test]
        public void DeviceId_ConsistentAcrossSessions()
        {
            string deviceId1 = _manager.DeviceId;
            _manager.StartNewSession();
            Assert.AreEqual(deviceId1, _manager.DeviceId);
        }

        [Test]
        public void ResetDeviceId_GeneratesNewDeviceId()
        {
            string originalDeviceId = _manager.DeviceId;
            _manager.ResetDeviceId();
            Assert.AreNotEqual(originalDeviceId, _manager.DeviceId);
        }

        [Test]
        public void SessionStart_IsRecent()
        {
            var now = System.DateTime.UtcNow;
            var diff = (now - _manager.SessionStart).TotalSeconds;
            Assert.Less(diff, 5); // Should be within 5 seconds
        }
    }
}
