namespace OzxApm.Models
{
    public enum Platform
    {
        Unknown,
        Android,
        iOS,
        Windows,
        MacOS,
        Linux,
        WebGL
    }

    public enum NetworkType
    {
        Unknown,
        WiFi,
        Cellular2G,
        Cellular3G,
        Cellular4G,
        Cellular5G,
        Ethernet,
        None
    }

    public enum EventType
    {
        PerfSample,
        Jank,
        Startup,
        SceneLoad,
        AssetLoad,
        Http,
        Exception,
        Crash
    }

    /// <summary>
    /// Build type sent with every event (APM-5). Auto: Editor when running in the
    /// Unity Editor, Dev in a development build (Debug.isDebugBuild), else Release.
    /// The admin shows Release by default.
    /// </summary>
    public enum ApmBuildType
    {
        Auto = 0,
        Release = 1,
        Dev = 2,
        Editor = 3
    }

    public enum LogLevel
    {
        None = 0,
        Error = 1,
        Warning = 2,
        Info = 3,
        Debug = 4
    }

    /// <summary>
    /// The player's consent for one ConsentCategory (APM-14). Only Granted
    /// collects: while a category is Undecided or Denied nothing of it is
    /// enqueued, written to disk or uploaded. Denied also deletes what is
    /// stored locally. See ApmClient.SetConsent.
    /// </summary>
    public enum ConsentState
    {
        /// <summary>No choice yet: collect nothing, keep what is already stored.</summary>
        Undecided = 0,
        /// <summary>Pre-APM-14 name of Undecided.</summary>
        Unknown = 0,
        Granted = 1,
        /// <summary>Collect nothing and delete what is stored locally.</summary>
        Denied = 2
    }

    /// <summary>
    /// What a consent covers (APM-14). Each category has its own state.
    /// </summary>
    public enum ConsentCategory
    {
        /// <summary>
        /// Technical diagnostics: exceptions, crashes, performance samples,
        /// jank, startup and scene-load timings. Everything the SDK collects today.
        /// </summary>
        Technical = 0,
        /// <summary>
        /// Analytics / operations events (APM-12: retention, funnels, ads).
        /// The SDK collects none yet; the state is stored and reported so the game
        /// can ask for it now, and APM-12 events will obey it.
        /// </summary>
        Analytics = 1
    }
}
