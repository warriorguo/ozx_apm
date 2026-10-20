using System;

namespace OzxApm.Core
{
    /// <summary>
    /// Ad events (APM-12; OZX-1280: rewarded video): ops events with fixed
    /// names and property keys, so the BI dashboards (impressions per DAU,
    /// ARPDAU by country and placement, the per-placement funnel, retention by
    /// ad views, reconciliation with the networks' reports) work without
    /// configuration. Like every ops event: analytics consent only, never
    /// sampled or rate limited, stored once on the server whatever is re-sent.
    ///
    /// The funnel of a placement, in order:
    /// <code>
    /// ApmAds.EntryShown("revive");                  // the offer/button became visible
    /// ApmAds.Click("revive");                       // the player tapped it
    /// ApmAds.Request("revive", "admob");            // an ad was requested (optional)
    /// ApmAds.Show("revive", "admob", revenue, "USD"); // an impression, with the ad SDK's estimated revenue
    /// ApmAds.Complete("revive", "admob");           // watched to the end
    /// ApmAds.Reward("revive", "admob");             // the reward was granted
    /// ApmAds.Fail("revive", "admob", "no_fill");    // any failure (load, show), with a short reason
    /// </code>
    /// placement: the game's name of the ad slot ("revive", "double_loot");
    /// network: the ad network that served it ("admob", "applovin", "unity":
    /// lower case, the same names as in the imported network reports). The
    /// run context the dashboards slice by (chapter, room, revives used) comes
    /// from the game context: set it with ApmClient.SetContext("chapter", ...),
    /// ("room", ...), ("revivesUsed", ...) during the run (the server keeps the
    /// keys in its ops context whitelist). Never put personal data in any value.
    ///
    /// Every method returns false when nothing was recorded (no consent, not
    /// initialized). Any thread. For more properties use
    /// ApmClient.OpsEvent(ApmAds.ShowEvent) with the same keys.
    /// </summary>
    public static class ApmAds
    {
        public const string EntryShowEvent = "ad_entry_show";
        public const string ClickEvent = "ad_click";
        public const string RequestEvent = "ad_request";
        public const string ShowEvent = "ad_show";
        public const string CompleteEvent = "ad_complete";
        public const string RewardEvent = "ad_reward";
        public const string FailEvent = "ad_fail";

        /// <summary>Property keys.</summary>
        public const string PlacementKey = "placement";
        public const string NetworkKey = "network";
        public const string RevenueKey = "revenue";
        public const string CurrencyKey = "currency";
        public const string PrecisionKey = "precision";
        public const string ReasonKey = "reason";
        public const string AdTypeKey = "ad_type";

        /// <summary>Decimals of the revenue per impression (a few millionths of a dollar matter).</summary>
        public const int RevenueDecimals = 6;

        /// <summary>The ad offer (button, popup) of a placement became visible: the funnel's first step.</summary>
        public static bool EntryShown(string placement) => Base(EntryShowEvent, placement, null).Send();

        /// <summary>The player chose to watch (tapped the offer).</summary>
        public static bool Click(string placement) => Base(ClickEvent, placement, null).Send();

        /// <summary>An ad was requested from the network (optional step).</summary>
        public static bool Request(string placement, string network) => Base(RequestEvent, placement, network).Send();

        /// <summary>
        /// An impression: the ad started showing. revenue: what the ad SDK
        /// estimated this impression earned (its paid/revenue callback), in
        /// <paramref name="currency"/> (ISO 4217, e.g. "USD"; null or empty:
        /// USD) units, not micros; NaN or negative: unknown (left out).
        /// precision: what the SDK says about the value ("estimated",
        /// "publisher_defined", "precise"), optional. adType defaults to
        /// "rewarded".
        /// </summary>
        public static bool Show(string placement, string network, double revenue = double.NaN, string currency = null,
            string precision = null, string adType = "rewarded")
        {
            var e = Base(ShowEvent, placement, network);
            if (!e.IsActive)
                return false;
            if (!double.IsNaN(revenue) && !double.IsInfinity(revenue) && revenue >= 0)
            {
                e = e.Num(RevenueKey, revenue, RevenueDecimals)
                     .Str(CurrencyKey, string.IsNullOrEmpty(currency) ? "USD" : currency.Trim().ToUpperInvariant());
            }
            return e.Str(PrecisionKey, precision).Str(AdTypeKey, adType).Send();
        }

        /// <summary>The ad was watched to the end.</summary>
        public static bool Complete(string placement, string network) => Base(CompleteEvent, placement, network).Send();

        /// <summary>The reward was granted to the player.</summary>
        public static bool Reward(string placement, string network) => Base(RewardEvent, placement, network).Send();

        /// <summary>Loading or showing failed; reason: a short code ("no_fill", "timeout", "show_failed"), never a raw SDK message with ids.</summary>
        public static bool Fail(string placement, string network, string reason) =>
            Base(FailEvent, placement, network).Str(ReasonKey, reason).Send();

        private static ApmEvent Base(string name, string placement, string network)
        {
            return ApmClient.OpsEvent(name).Str(PlacementKey, placement).Str(NetworkKey, NormalizeNetwork(network));
        }

        private static string NormalizeNetwork(string network)
        {
            return string.IsNullOrEmpty(network) ? network : network.Trim().ToLowerInvariant();
        }
    }
}
