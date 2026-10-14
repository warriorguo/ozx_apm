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

    public enum LogLevel
    {
        None = 0,
        Error = 1,
        Warning = 2,
        Info = 3,
        Debug = 4
    }

    /// <summary>
    /// Player consent for data collection. Checked before an event is
    /// enqueued, before anything is written to disk and before uploading.
    /// Only Granted collects. The full consent feature (persisted state,
    /// clearing local data on Denied, per-category control) is APM-14.
    /// </summary>
    public enum ConsentState
    {
        Unknown = 0,
        Granted = 1,
        Denied = 2
    }
}
