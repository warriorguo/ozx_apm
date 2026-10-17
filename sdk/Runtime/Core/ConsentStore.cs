using UnityEngine;
using OzxApm.Models;

namespace OzxApm.Core
{
    /// <summary>
    /// The player's consent choices in PlayerPrefs (APM-14), one int per
    /// category (0 Undecided, 1 Granted, 2 Denied). Only an explicit
    /// ApmClient.SetConsent writes; a missing key means "no choice yet" and the
    /// ApmConfig default applies. Main thread (PlayerPrefs).
    /// </summary>
    internal static class ConsentStore
    {
        internal const string TechnicalKey = "ozx_apm_consent_technical";
        internal const string AnalyticsKey = "ozx_apm_consent_analytics";

        private static string Key(ConsentCategory category)
        {
            return category == ConsentCategory.Analytics ? AnalyticsKey : TechnicalKey;
        }

        internal static bool IsValid(ConsentState state)
        {
            return state == ConsentState.Undecided || state == ConsentState.Granted || state == ConsentState.Denied;
        }

        /// <summary>The stored choice, if there is a valid one.</summary>
        internal static bool TryLoad(ConsentCategory category, out ConsentState state)
        {
            state = ConsentState.Undecided;
            string key = Key(category);
            if (!PlayerPrefs.HasKey(key))
                return false;
            var value = (ConsentState)PlayerPrefs.GetInt(key, -1);
            if (!IsValid(value))
                return false;
            state = value;
            return true;
        }

        /// <summary>Stores the choice; returns true if PlayerPrefs changed (the caller saves).</summary>
        internal static bool Set(ConsentCategory category, ConsentState state)
        {
            string key = Key(category);
            if (PlayerPrefs.HasKey(key) && PlayerPrefs.GetInt(key, -1) == (int)state)
                return false;
            PlayerPrefs.SetInt(key, (int)state);
            return true;
        }

        /// <summary>Test seam: forget the stored choices.</summary>
        internal static void DeleteForTests()
        {
            PlayerPrefs.DeleteKey(TechnicalKey);
            PlayerPrefs.DeleteKey(AnalyticsKey);
        }
    }
}
