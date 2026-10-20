using System;
using System.Threading;
using UnityEngine;

namespace OzxApm.Core
{
    /// <summary>
    /// The anonymous player id of operations events (APM-12): 32 random hex
    /// digits generated on the first launch, stored in PlayerPrefs only with
    /// analytics consent, and the same on every launch after that, so the
    /// server can count a player across sessions and days (DAU, retention).
    ///
    /// Separate from the device id of the technical data (APM-14): the
    /// technical tables never see it and the ops tables never see the device
    /// id. Neither is derived from the device or the other; a reinstall (or
    /// PlayerPrefs.DeleteAll) is a new player. ResetPlayerId makes a new one;
    /// ResetDeviceId does not touch it. Analytics Denied removes it from
    /// PlayerPrefs (kept in memory for the process; a launch that stays
    /// denied gets a new one), like the device id with technical consent.
    /// </summary>
    internal static class PlayerIdentity
    {
        internal const string Key = "ozx_apm_player_id";

        private static readonly object s_lock = new object();
        private static volatile string s_id;
        private static bool s_persisted;

        /// <summary>The current id (generated if there is none yet). Main thread the first time (PlayerPrefs).</summary>
        internal static string Get(bool persist)
        {
            lock (s_lock)
            {
                if (string.IsNullOrEmpty(s_id))
                {
                    string stored = PlayerPrefs.GetString(Key, "");
                    s_persisted = IsValid(stored);
                    s_id = s_persisted ? stored : NewId();
                }
                if (persist)
                    PersistLocked();
                return s_id;
            }
        }

        /// <summary>The id if one was read or generated already (any thread), else null.</summary>
        internal static string Current => s_id;

        /// <summary>Writes the id to PlayerPrefs if it is not stored yet. Main thread.</summary>
        internal static void Persist()
        {
            lock (s_lock)
                PersistLocked();
        }

        private static void PersistLocked()
        {
            if (s_persisted || string.IsNullOrEmpty(s_id))
                return;
            PlayerPrefs.SetString(Key, s_id);
            PlayerPrefs.Save();
            s_persisted = true;
        }

        /// <summary>
        /// Analytics denied: removes the stored id (the one in memory stays for
        /// this process). PlayerPrefs.Save only if a key was removed. Main thread.
        /// </summary>
        internal static void Forget()
        {
            lock (s_lock)
            {
                s_persisted = false;
                if (!PlayerPrefs.HasKey(Key))
                    return;
                PlayerPrefs.DeleteKey(Key);
                PlayerPrefs.Save();
            }
        }

        /// <summary>A new id; stored only with persist, else the old one is removed. Main thread.</summary>
        internal static string Reset(bool persist)
        {
            lock (s_lock)
            {
                s_id = NewId();
                s_persisted = false;
                if (persist)
                {
                    PersistLocked();
                }
                else if (PlayerPrefs.HasKey(Key))
                {
                    PlayerPrefs.DeleteKey(Key);
                    PlayerPrefs.Save();
                }
                return s_id;
            }
        }

        internal static bool IsValid(string id)
        {
            if (string.IsNullOrEmpty(id) || id.Length > 64)
                return false;
            for (int i = 0; i < id.Length; i++)
            {
                char c = id[i];
                if (!(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'))
                    return false;
            }
            return true;
        }

        private static string NewId() => Guid.NewGuid().ToString("N");

        internal static void ClearCacheForTests()
        {
            lock (s_lock)
            {
                s_id = null;
                s_persisted = false;
            }
        }
    }

    /// <summary>
    /// The coarse country ops events carry (APM-12): ApmConfig.Country, else
    /// the device's region setting on Android (java.util.Locale.getDefault()
    /// .getCountry(): a setting, not a location), else none. Two letters
    /// (ISO 3166-1 alpha-2) or nothing.
    /// </summary>
    internal static class PlayerCountry
    {
        /// <summary>Main thread (Initialize): one JNI call on Android when the config sets none.</summary>
        internal static string Resolve(string configured)
        {
            if (configured != null)
                return Normalize(configured);
#if UNITY_ANDROID && !UNITY_EDITOR
            try
            {
                using (var locale = new AndroidJavaClass("java.util.Locale"))
                using (var current = locale.CallStatic<AndroidJavaObject>("getDefault"))
                    return Normalize(current?.Call<string>("getCountry"));
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("PlayerCountry", ex);
            }
#endif
            return null;
        }

        /// <summary>Two ASCII letters, upper case; anything else (a region code like "419") is null.</summary>
        internal static string Normalize(string c)
        {
            if (c == null)
                return null;
            c = c.Trim();
            if (c.Length != 2)
                return null;
            char a = char.ToUpperInvariant(c[0]), b = char.ToUpperInvariant(c[1]);
            if (a < 'A' || a > 'Z' || b < 'A' || b > 'Z')
                return null;
            return new string(new[] { a, b });
        }
    }
}
