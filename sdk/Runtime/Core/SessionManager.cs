using System;
using System.Globalization;
using UnityEngine;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>
    /// Manages device ID and session lifecycle
    /// </summary>
    public class SessionManager
    {
        private const string SessionIdKey = "ozx_apm_session_id";
        private const string SessionStartKey = "ozx_apm_session_start";

        private string _deviceId;
        private string _sessionId;
        private string _userId;
        private DateTime _sessionStart;
        private DateTime _lastActivity;
        private bool _isBackground;

        /// <summary>
        /// Session timeout threshold for background splits (seconds)
        /// </summary>
        public float BackgroundTimeoutSeconds { get; set; } = 30f;

        public string DeviceId => _deviceId;
        public string SessionId => _sessionId;
        public string UserId => _userId;
        public DateTime SessionStart => _sessionStart;

        // Returns whether PlayerPrefs may be written (consent). Null: always.
        private readonly Func<bool> _canPersist;
        private bool _sessionPersisted;

        public SessionManager() : this(null)
        {
        }

        /// <param name="canPersist">
        /// Checked before every PlayerPrefs write. While it returns false (no
        /// consent) ids live in memory only; call OnConsentGranted to store them.
        /// </param>
        public SessionManager(Func<bool> canPersist)
        {
            _canPersist = canPersist;
            _deviceId = DeviceInfo.GetDeviceId(CanPersist);
            _lastActivity = DateTime.UtcNow;
            StartNewSession();
        }

        private bool CanPersist => _canPersist == null || _canPersist();

        /// <summary>
        /// Writes the ids that were held back while consent was missing.
        /// </summary>
        public void OnConsentGranted()
        {
            if (!CanPersist)
                return;
            DeviceInfo.PersistDeviceId();
            if (!_sessionPersisted)
                PersistSession();
        }

        /// <summary>
        /// Technical consent denied (APM-14): removes the stored device and session
        /// ids from PlayerPrefs. They stay in memory for this process (and are
        /// stored again if consent is granted); a launch that stays denied starts
        /// with new ones. Saves PlayerPrefs only if something was removed.
        /// </summary>
        public void OnConsentDenied()
        {
            bool removed = false;
            foreach (var key in new[] { SessionIdKey, SessionStartKey })
            {
                if (!PlayerPrefs.HasKey(key))
                    continue;
                PlayerPrefs.DeleteKey(key);
                removed = true;
            }
            if (removed)
                PlayerPrefs.Save();
            _sessionPersisted = false;
            DeviceInfo.ForgetStoredDeviceId();
        }

        /// <summary>
        /// Sets the user ID for correlation
        /// </summary>
        public void SetUserId(string userId)
        {
            _userId = userId;
        }

        /// <summary>
        /// Clears the user ID
        /// </summary>
        public void ClearUserId()
        {
            _userId = null;
        }

        /// <summary>
        /// Resets the device ID for privacy compliance
        /// </summary>
        public void ResetDeviceId()
        {
            DeviceInfo.ResetDeviceId(CanPersist);
            _deviceId = DeviceInfo.GetDeviceId(CanPersist);
        }

        /// <summary>
        /// Called when app enters background
        /// </summary>
        public void OnApplicationPause(bool paused)
        {
            if (paused)
            {
                _isBackground = true;
                _lastActivity = DateTime.UtcNow;
            }
            else
            {
                _isBackground = false;
                CheckSessionTimeout();
            }
        }

        /// <summary>
        /// Called when app gains/loses focus
        /// </summary>
        public void OnApplicationFocus(bool hasFocus)
        {
            if (!hasFocus)
            {
                _lastActivity = DateTime.UtcNow;
            }
            else
            {
                CheckSessionTimeout();
            }
        }

        /// <summary>
        /// Records activity to prevent session timeout
        /// </summary>
        public void RecordActivity()
        {
            _lastActivity = DateTime.UtcNow;
        }

        /// <summary>
        /// Starts a new session
        /// </summary>
        public void StartNewSession()
        {
            _sessionId = GenerateSessionId();
            _sessionStart = DateTime.UtcNow;
            _lastActivity = _sessionStart;
            _sessionPersisted = false;

            // Persist session info for crash recovery (only with consent)
            if (CanPersist)
                PersistSession();
        }

        private void PersistSession()
        {
            PlayerPrefs.SetString(SessionIdKey, _sessionId);
            PlayerPrefs.SetString(SessionStartKey, _sessionStart.ToString("o", CultureInfo.InvariantCulture));
            PlayerPrefs.Save();
            _sessionPersisted = true;
        }

        /// <summary>
        /// Gets session duration in seconds
        /// </summary>
        public float GetSessionDurationSeconds()
        {
            return (float)(DateTime.UtcNow - _sessionStart).TotalSeconds;
        }

        private void CheckSessionTimeout()
        {
            if (_isBackground)
                return;

            float backgroundTime = (float)(DateTime.UtcNow - _lastActivity).TotalSeconds;
            if (backgroundTime > BackgroundTimeoutSeconds)
            {
                StartNewSession();
            }
        }

        private string GenerateSessionId()
        {
            // Format: timestamp_randomhex
            long timestamp = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            string random = Guid.NewGuid().ToString("N").Substring(0, 8);
            return $"{timestamp}_{random}";
        }

        /// <summary>
        /// Tries to recover session from previous run (for crash correlation)
        /// </summary>
        public bool TryRecoverPreviousSession(out string sessionId, out DateTime sessionStart)
        {
            sessionId = PlayerPrefs.GetString(SessionIdKey, "");
            string startStr = PlayerPrefs.GetString(SessionStartKey, "");

            if (!string.IsNullOrEmpty(sessionId) &&
                DateTime.TryParse(startStr, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind, out sessionStart))
            {
                return true;
            }

            sessionStart = DateTime.MinValue;
            return false;
        }
    }
}
