using System;
using System.Collections.Generic;
using Unity.Profiling;
using UnityEngine;
using UnityEngine.SceneManagement;
using OzxApm.Models;
using OzxApm.Collectors;
using OzxApm.Network;
using OzxApm.Utils;

namespace OzxApm.Core
{
    /// <summary>
    /// Main entry point for OZX APM SDK
    /// </summary>
    public class ApmClient : MonoBehaviour
    {
        /// <summary>
        /// Prefix of every console message the SDK writes. ExceptionCollector ignores
        /// messages with this prefix so the SDK never reports itself.
        /// </summary>
        internal const string LogPrefix = "[OzxApm]";

        /// <summary>SDK version; sent with every upload so the server can tell clients apart.</summary>
        public const string SdkVersion = "2.0.0";
        internal const string SdkVersionHeader = "X-Apm-Sdk-Version";

        // How long lifecycle events may wait for the storage worker to write to disk.
        private const int PauseFlushTimeoutMs = 100;
        private const int ShutdownFlushTimeoutMs = 500;
        private const int TerminationFlushTimeoutMs = 500;
        private const int MaxReportedInternalErrors = 10;

        private static ApmClient _instance;
        private static readonly object _lock = new object();
        private static bool _isInitialized;
        private static bool _isShuttingDown;

        // Read by Log() from any thread; avoids touching the UnityEngine.Object off the main thread.
        private static volatile ApmConfig _logConfig;
        // The game's latest SetConsent per ConsentCategory in this process (-1: none);
        // it wins over the stored choice and the config default (ResolveConsent).
        private static readonly int[] _explicitConsent = { -1, -1 };
        private static bool _resetDeviceIdBeforeInit;
        private static readonly ApmConfig s_builtInDefaults = new ApmConfig();
        private static int _internalErrorCount;

        private ApmConfig _config;
        private SessionManager _sessionManager;
        private EventReporter _reporter;
        private OfflineStorage _offlineStorage;
        private NetworkLogger _networkLogger;

        // Collectors
        private PerformanceCollector _performanceCollector;
        private JankDetector _jankDetector;
        private MemoryCollector _memoryCollector;
        private StartupTracker _startupTracker;
        private SceneLoadTracker _sceneLoadTracker;
        private ExceptionCollector _exceptionCollector;
        private CustomCollector _customCollector;
        // Breadcrumbs and recent warnings (APM-6), shared by the collectors.
        private Timeline _timeline;
        // Crash journal of this process and report of earlier ones (APM-7);
        // null where crash reporting is off (not Android, or disabled).
        private CrashMonitor _crash;
        private string _crashDir;
        // Memory readings and game runs (APM-9).
        private RunTracker _runs;
        private MemorySampler _memory;
        private static volatile RunTracker s_runs;

        // Read by the static API from any thread (TrackEvent, RecordMetric,
        // timers, RecordBreadcrumb) without touching the UnityEngine.Object.
        private static volatile CustomCollector s_custom;
        private static volatile Timeline s_timeline;
        private static volatile bool s_collecting;

        private List<ICollector> _collectors = new List<ICollector>();
        private string _currentScene;
        private bool _isStopping;
        // Collectors run (technical consent Granted). Main thread.
        private bool _collecting;

        // Static per device: read once at Initialize instead of per event
        // (SystemInfo strings cross into native code and allocate on every call).
        private string _platform;
        private string _deviceModel;
        private string _osVersion;
        private string _buildType;
        private string _gpu;
        private string _ramClass;
        // Refreshed on the main thread every NetTypePollSeconds (and on resume);
        // read by EnqueueEvent, which may run on another thread. Constant strings.
        private volatile string _netType;
        private float _nextNetTypeCheck;
        private const float NetTypePollSeconds = 5f;
        private bool _uploadsEnabled;

        public static ApmClient Instance
        {
            get
            {
                if (_isShuttingDown)
                    return null;

                lock (_lock)
                {
                    if (_instance == null)
                    {
                        // Try to find existing instance
                        _instance = FindObjectOfType<ApmClient>();

                        if (_instance == null)
                        {
                            // Create new GameObject
                            var go = new GameObject("[OzxApm]");
                            _instance = go.AddComponent<ApmClient>();
                            // Only valid in play mode (EditMode tests create the client too).
                            if (Application.isPlaying)
                                DontDestroyOnLoad(go);
                        }
                    }
                    return _instance;
                }
            }
        }

        public static bool IsInitialized => _isInitialized;
        public ApmConfig Config => _config;
        public SessionManager Session => _sessionManager;
        internal Timeline Timeline => _timeline;
        public NetworkLogger NetworkLog => _networkLogger;

        /// <summary>
        /// Initializes the APM SDK with the given configuration.
        /// Does no disk or network I/O on the calling thread beyond PlayerPrefs:
        /// stored events are scanned and uploaded in the background.
        /// </summary>
        public static void Initialize(ApmConfig config)
        {
            if (_isInitialized)
            {
                Log(LogLevel.Warning, "APM SDK already initialized");
                return;
            }

            if (config == null)
            {
                config = ApmConfig.Default();
            }

            try
            {
                Instance?.InitializeInternal(config);
            }
            catch (Exception ex)
            {
                // The SDK must never break the game, not even on a bad config.
                ReportInternalError("Initialize", ex);
            }
        }

        /// <summary>
        /// Initializes the APM SDK with default configuration
        /// </summary>
        public static void Initialize(string serverUrl, string appKey, string appVersion)
        {
            var config = ApmConfig.Default();
            config.ServerUrl = serverUrl;
            config.AppKey = appKey;
            config.AppVersion = appVersion;
            Initialize(config);
        }

        // ------------------------------------------------------------------
        // Privacy consent (APM-14)
        //
        // Two categories, each Undecided / Granted / Denied (ConsentCategory,
        // ConsentState). Technical covers everything the SDK collects today;
        // Analytics is for APM-12 operations events (none collected yet).
        //
        // The state of a category is, in this order: the game's SetConsent call,
        // the choice stored in PlayerPrefs by an earlier SetConsent
        // (ApmConfig.RememberConsent), the ApmConfig default
        // (DefaultTechnicalConsent: Granted, DefaultAnalyticsConsent: Undecided;
        // ApmConfig.UseOptInConsent makes both Undecided).
        //
        // Technical not Granted: collectors are stopped (no per-frame work, no
        // allocation), nothing is enqueued, written or uploaded, and a request
        // under way is aborted. Undecided keeps what is already stored (it is
        // uploaded once consent is granted); Denied deletes it: queued and stored
        // events, quarantined batches, the sequence file, the event directory,
        // the network debug log, and the device/session ids in PlayerPrefs.
        // ------------------------------------------------------------------

        /// <summary>
        /// Technical consent (pre-APM-14 single consent). Same as
        /// GetConsent(ConsentCategory.Technical).
        /// </summary>
        public static ConsentState Consent => GetConsent(ConsentCategory.Technical);

        /// <summary>
        /// Current consent of a category. Before Initialize: the game's
        /// SetConsent, else the stored choice, else the built-in default
        /// (Technical Granted, Analytics Undecided). Main thread.
        /// </summary>
        public static ConsentState GetConsent(ConsentCategory category)
        {
            try
            {
                var config = _logConfig;
                if (_isInitialized && config != null)
                    return config.GetConsent(category);
                return ResolveConsent(config ?? s_builtInDefaults, category);
            }
            catch (Exception ex)
            {
                ReportInternalError("GetConsent", ex);
                return ConsentState.Undecided;
            }
        }

        /// <summary>
        /// Sets the consent of every category (pre-APM-14 single consent).
        /// Same as SetConsent(category, state) for Technical and Analytics.
        /// </summary>
        public static void SetConsent(ConsentState state)
        {
            SetConsentInternal(true, true, state);
        }

        /// <summary>
        /// Sets the player's consent for one category. Takes effect at once and is
        /// stored for the next launches (ApmConfig.RememberConsent). Can be called
        /// before Initialize. Main thread.
        ///
        /// Technical: Granted starts collecting and uploading; Undecided stops
        /// both and keeps what is stored; Denied stops both and deletes every
        /// stored event and SDK file (asynchronously, on the storage thread,
        /// within milliseconds) and the stored device/session ids. Events still
        /// queued or being uploaded when it is called are never sent; a request
        /// whose bytes had already reached the server may have been stored there
        /// (delete it with the admin API, RELEASE.md APM-14). Calling it with
        /// Denied again deletes again.
        ///
        /// Analytics: only stored and reported today (GetConsent, GetStats); the
        /// SDK collects no analytics events yet (APM-12 will, and only when Granted).
        /// </summary>
        public static void SetConsent(ConsentCategory category, ConsentState state)
        {
            SetConsentInternal(category == ConsentCategory.Technical, category == ConsentCategory.Analytics, state);
        }

        private static void SetConsentInternal(bool technical, bool analytics, ConsentState state)
        {
            try
            {
                if (!ConsentStore.IsValid(state))
                {
                    Log(LogLevel.Warning, $"SetConsent: invalid state {(int)state} ignored");
                    return;
                }
                if (technical)
                    _explicitConsent[(int)ConsentCategory.Technical] = (int)state;
                if (analytics)
                    _explicitConsent[(int)ConsentCategory.Analytics] = (int)state;

                // Before Initialize the choice is applied (and stored) by Initialize.
                if (_isInitialized && !ReferenceEquals(_instance, null))
                    _instance.ApplyConsent(technical, analytics, state);
            }
            catch (Exception ex)
            {
                ReportInternalError("SetConsent", ex);
            }
        }

        /// <summary>The consent a category starts with (see the section comment).</summary>
        private static ConsentState ResolveConsent(ApmConfig config, ConsentCategory category)
        {
            int explicitState = _explicitConsent[(int)category];
            if (explicitState >= 0)
                return (ConsentState)explicitState;
            if (config.RememberConsent && ConsentStore.TryLoad(category, out var stored))
                return stored;
            var fallback = category == ConsentCategory.Analytics ? config.DefaultAnalyticsConsent : config.DefaultTechnicalConsent;
            return ConsentStore.IsValid(fallback) ? fallback : ConsentState.Undecided;
        }

        private void ApplyConsent(bool technical, bool analytics, ConsentState state)
        {
            // State first: from here on no thread enqueues, writes or uploads.
            if (analytics)
                _config.SetConsentState(ConsentCategory.Analytics, state);
            if (technical)
            {
                _config.SetConsentState(ConsentCategory.Technical, state);
                OnTechnicalConsent(state);
            }
            Log(LogLevel.Info, $"Consent {(technical && analytics ? "all" : technical ? "technical" : "analytics")}: {state}");
            StoreExplicitConsent();
        }

        private void OnTechnicalConsent(ConsentState state)
        {
            if (state == ConsentState.Granted)
            {
                _sessionManager.OnConsentGranted();
                SetCollecting(true);
                _reporter.RetryNow();
                return;
            }

            SetCollecting(false);
            _reporter.CancelInFlight();
            if (state == ConsentState.Denied)
                DeleteLocalTechnicalData();
        }

        /// <summary>Denied: nothing of the technical data stays on the device.</summary>
        private void DeleteLocalTechnicalData()
        {
            _timeline?.Clear();
            _offlineStorage.Clear();
            _networkLogger?.ClearLogs();
            _sessionManager.OnConsentDenied();
            // The crash journals (APM-7), also those left by earlier processes.
            if (_crash != null)
                _crash.DeleteAll();
            else
                CrashMonitor.DeleteDirectory(_crashDir);
        }

        /// <summary>Writes the game's SetConsent choices to PlayerPrefs (RememberConsent).</summary>
        private void StoreExplicitConsent()
        {
            if (!_config.RememberConsent)
                return;
            try
            {
                bool changed = false;
                for (int c = 0; c < _explicitConsent.Length; c++)
                {
                    if (_explicitConsent[c] >= 0)
                        changed |= ConsentStore.Set((ConsentCategory)c, (ConsentState)_explicitConsent[c]);
                }
                if (changed)
                    PlayerPrefs.Save();
            }
            catch (Exception ex)
            {
                ReportInternalError("StoreConsent", ex);
            }
        }

        /// <summary>
        /// The anonymous device id sent with every event: what the player gives
        /// support to have their data deleted (RELEASE.md, APM-14). Main thread.
        /// </summary>
        public static string GetDeviceId()
        {
            try
            {
                if (_isInitialized && !ReferenceEquals(_instance, null) && _instance._sessionManager != null)
                    return _instance._sessionManager.DeviceId;
                return DeviceInfo.GetDeviceId(false);
            }
            catch (Exception ex)
            {
                ReportInternalError("GetDeviceId", ex);
                return null;
            }
        }

        /// <summary>
        /// Replaces the anonymous device id with a new random one (APM-14) and
        /// returns it. Also starts a new session (session ids are random and never
        /// derived from the device id, so nothing links the old id to the new one),
        /// deletes every locally stored event (they carry the old id), aborts an
        /// upload under way, and restarts the event sequence at 1. The new id is
        /// stored only with technical consent. Data already on the server keeps the
        /// old id: get it with GetDeviceId first and delete it with the admin API.
        /// Works before Initialize. Main thread.
        /// </summary>
        public static string ResetDeviceId()
        {
            try
            {
                if (_isInitialized && !ReferenceEquals(_instance, null))
                    return _instance.ResetDeviceIdInternal();

                // Before Initialize: Initialize clears the stored events and stores
                // the new id if consent allows.
                _resetDeviceIdBeforeInit = true;
                DeviceInfo.ResetDeviceId(false);
                return DeviceInfo.GetDeviceId(false);
            }
            catch (Exception ex)
            {
                ReportInternalError("ResetDeviceId", ex);
                return null;
            }
        }

        private string ResetDeviceIdInternal()
        {
            _reporter.CancelInFlight();
            _offlineStorage.Clear(true);
            _sessionManager.ResetDeviceId();
            _sessionManager.StartNewSession();
            // Journals of earlier processes carry the old id (APM-7).
            _crash?.OnDeviceIdReset();
            Log(LogLevel.Info, "Device id reset; local events deleted");
            return _sessionManager.DeviceId;
        }

        /// <summary>
        /// Sets the user ID for correlation
        /// </summary>
        public static void SetUserId(string userId)
        {
            if (_isInitialized)
            {
                Instance._sessionManager.SetUserId(userId);
            }
        }

        /// <summary>
        /// Clears the user ID
        /// </summary>
        public static void ClearUserId()
        {
            if (_isInitialized)
            {
                Instance._sessionManager.ClearUserId();
            }
        }

        /// <summary>
        /// Sets a game context value (APM-5): every event enqueued afterwards, of
        /// any type, carries the context as it is at that moment, and it is stored
        /// with the event on disk. Use it for what you want to filter and group by
        /// on the server: level, room, seed, loadout, data version, git commit...
        ///
        /// A null or empty value removes the key. Keys: 1-64 characters from
        /// [A-Za-z0-9_.-]; values are cut to 256 characters; at most 64 keys and
        /// about 4KB in total (GameContext has the limits). Returns false if the
        /// value was refused (a warning is logged, the counter in GetStats goes up,
        /// and a previous value of the key is removed); never throws.
        ///
        /// Works before Initialize and from any thread. Setting a key to the value
        /// it already has costs nothing; otherwise the context is copied once, at
        /// the next event. Do not use it for values that change every frame.
        /// </summary>
        public static bool SetContext(string key, string value)
        {
            try
            {
                return GameContext.Set(key, value);
            }
            catch (Exception ex)
            {
                ReportInternalError("SetContext", ex);
                return false;
            }
        }

        /// <summary>Removes a game context key. Returns true if it was set.</summary>
        public static bool RemoveContext(string key)
        {
            try
            {
                return GameContext.Remove(key);
            }
            catch (Exception ex)
            {
                ReportInternalError("RemoveContext", ex);
                return false;
            }
        }

        /// <summary>Removes every game context key (e.g. at the end of a run).</summary>
        public static void ClearContext()
        {
            try
            {
                GameContext.Clear();
            }
            catch (Exception ex)
            {
                ReportInternalError("ClearContext", ex);
            }
        }

        /// <summary>The current value of a game context key, or null.</summary>
        public static string GetContext(string key)
        {
            return GameContext.Current.TryGetValue(key, out string value) ? value : null;
        }

        /// <summary>
        /// Sets a context key to the count range of a number (APM-9), e.g. the
        /// alive enemy count: "0", "1-5", "6-10", "11-20", "21-30", "31-50",
        /// "51-100", "101-200", "201+" (the server groups raw numbers into the
        /// same ranges). The context only changes when the count enters another
        /// range, so calling it whenever the count changes, even every frame,
        /// copies nothing and allocates nothing. Any thread; works before
        /// Initialize. Returns false for an invalid key.
        /// </summary>
        public static bool SetContextCount(string key, long count)
        {
            try
            {
                return GameContext.Set(key, GameContext.CountBucket(count));
            }
            catch (Exception ex)
            {
                ReportInternalError("SetContextCount", ex);
                return false;
            }
        }

        // ------------------------------------------------------------------
        // Game runs (APM-9)
        //
        // A run is one play-through between BeginRun and EndRun (for OZX: a
        // dungeon run). While it is open, the context key "runId" is the run's
        // id (every event of the run carries it), and the SDK keeps the peak
        // of every memory figure it reads (the perf sample's breakdown and,
        // on Android, the process RSS). EndRun sends one "run" event with the
        // peaks, the length and the result. If the process dies during a run
        // (an OOM kill, a crash), the crash journal has the run and its peaks:
        // the next launch sends the run (end reason "crash" with the crash
        // type, or "process_end") and the crash's details carry the run's
        // peaks and the last memory reading.
        //
        // Technical data: without technical consent the run is tracked in
        // memory but nothing is sent or written (APM-14).
        // ------------------------------------------------------------------

        /// <summary>
        /// Starts a game run with the game's id for it (1-64 printable
        /// characters; e.g. the run sequence number or seed: never personal
        /// data). A run still open is ended first (end reason "new_run") and
        /// sent. Sets the context key "runId". Any thread; returns false before
        /// Initialize or for an invalid id. Allocates the ended run's event.
        /// </summary>
        public static bool BeginRun(string runId)
        {
            try
            {
                var runs = s_runs;
                var client = _instance;
                if (runs == null || !_isInitialized || ReferenceEquals(client, null))
                    return false;
                if (!RunTracker.IsValidRunId(runId))
                {
                    Log(LogLevel.Warning, "BeginRun: the run id must be 1-64 printable characters; not started");
                    return false;
                }
                var previous = runs.Begin(runId);
                if (previous != null)
                    client.EnqueueRun(previous); // with the previous run's context
                if (!GameContext.Set(RunTracker.ContextKey, runId))
                    Log(LogLevel.Warning, "BeginRun: the context key runId could not be set (context full); the run is tracked, its events lack runId");
                CrashJournal.NotifyChanged();
                return true;
            }
            catch (Exception ex)
            {
                ReportInternalError("BeginRun", ex);
                return false;
            }
        }

        /// <summary>
        /// Ends the open run and sends it with its peak memory, length and the
        /// optional result ([A-Za-z0-9_.-]{1,64}, e.g. "win", "death", "quit";
        /// an invalid one is left out). Removes the context key "runId" (if it
        /// still is the run's id). Returns false when no run is open. Any thread.
        /// </summary>
        public static bool EndRun(string result = null)
        {
            try
            {
                var runs = s_runs;
                var client = _instance;
                if (runs == null || !_isInitialized || ReferenceEquals(client, null))
                    return false;
                if (result != null && !GameContext.IsValidKey(result))
                {
                    Log(LogLevel.Warning, "EndRun: the result must be [A-Za-z0-9_.-]{1,64}; sent without it");
                    result = null;
                }
                var evt = runs.End(RunEvent.EndEnd, result);
                if (evt == null)
                    return false;
                client.EnqueueRun(evt);
                if (GameContext.Current.TryGetValue(RunTracker.ContextKey, out string current) && current == evt.run_id)
                    GameContext.Remove(RunTracker.ContextKey);
                CrashJournal.NotifyChanged();
                return true;
            }
            catch (Exception ex)
            {
                ReportInternalError("EndRun", ex);
                return false;
            }
        }

        /// <summary>The open run's id, or null.</summary>
        public static string CurrentRunId => s_runs?.CurrentRunId;

        /// <summary>
        /// Marks Time To Interactive. Also tells the SDK the game has finished
        /// loading, so uploads may start before InitialUploadDelaySeconds.
        /// </summary>
        public static void MarkTTI()
        {
            if (!_isInitialized)
                return;

            try
            {
                var client = Instance;
                client._startupTracker?.MarkTTI();
                client._reporter?.NotifyGameReady();
            }
            catch (Exception ex)
            {
                ReportInternalError("MarkTTI", ex);
            }
        }

        /// <summary>
        /// Records a breadcrumb: a text later exceptions and janks carry on
        /// their time line. Ignored without technical consent. No allocation;
        /// any thread.
        /// </summary>
        public static void RecordBreadcrumb(string message)
        {
            var timeline = s_timeline;
            if (timeline != null && s_collecting)
                timeline.RecordText(message);
        }

        // ------------------------------------------------------------------
        // Custom events, metrics and timers (APM-6)
        //
        // Technical data, for diagnostics: collected only with technical
        // consent (ApmConfig.EnableCustomEvents, on by default). Names and
        // property keys: 1-64 characters from [A-Za-z0-9_.-]. Property values:
        // up to 256 characters, at most 64 properties and about 4KB per event
        // (the game context's limits). Never put personal data (names, e-mail
        // addresses, account or payment ids, chat or other input text, precise
        // location) in names, properties or units: they are stored as sent.
        // Business/operations events (retention, funnels, purchases) are not
        // these: they belong to APM-12's analytics events.
        //
        // Every call works from any thread, before Initialize too (then it
        // does nothing and returns false), and never throws.
        // ------------------------------------------------------------------

        /// <summary>
        /// Records a custom event (e.g. "room_enter"): it goes onto the
        /// breadcrumb time line that later exceptions and janks carry, and is
        /// uploaded with the current game context. The first
        /// ApmConfig.MaxCustomEventsPerMinute events of a minute are sent one by
        /// one; further ones are sent as one count per name and context.
        /// Allocates the event (about 150 bytes). Returns false if it was not
        /// recorded (no consent, disabled, invalid name).
        /// </summary>
        public static bool TrackEvent(string name)
        {
            return TrackEventInternal(name, null, 0);
        }

        /// <summary>A custom event with one property. Allocates the event and one 2-element array.</summary>
        public static bool TrackEvent(string name, string key, string value)
        {
            var custom = s_custom;
            if (custom == null || !custom.IsActive)
                return false;
            return TrackEventInternal(name, new[] { key, value }, 2);
        }

        /// <summary>A custom event with two properties. Allocates the event and one 4-element array.</summary>
        public static bool TrackEvent(string name, string key1, string value1, string key2, string value2)
        {
            var custom = s_custom;
            if (custom == null || !custom.IsActive)
                return false;
            return TrackEventInternal(name, new[] { key1, value1, key2, value2 }, 4);
        }

        /// <summary>
        /// A custom event with properties (copied; invalid keys and empty values
        /// are left out, values cut to 256 characters, at most 64 properties and
        /// about 4KB: see the section comment). Allocates the event and one array
        /// of 2 x count strings (plus the enumerator unless props is a Dictionary).
        /// </summary>
        public static bool TrackEvent(string name, IDictionary<string, string> props)
        {
            var custom = s_custom;
            if (custom == null || !custom.IsActive)
                return false;
            try
            {
                if (props == null || props.Count == 0)
                    return TrackEventInternal(name, null, 0);
                var pairs = new string[2 * props.Count];
                int n = 0;
                if (props is Dictionary<string, string> dict)
                {
                    foreach (var kv in dict)
                    {
                        if (n + 1 >= pairs.Length)
                            break;
                        pairs[n++] = kv.Key;
                        pairs[n++] = kv.Value;
                    }
                }
                else
                {
                    foreach (var kv in props)
                    {
                        if (n + 1 >= pairs.Length)
                            break;
                        pairs[n++] = kv.Key;
                        pairs[n++] = kv.Value;
                    }
                }
                return TrackEventInternal(name, pairs, n);
            }
            catch (Exception ex)
            {
                // E.g. the game changed the dictionary on another thread.
                ReportInternalError("TrackEvent", ex);
                return false;
            }
        }

        /// <summary>
        /// Starts a custom event with typed properties (APM-10): numbers,
        /// booleans and 2D vectors as well as strings, sent with
        /// <see cref="ApmEvent.Send"/>. For events shared with OZX's
        /// AiTelemetry, the name is "&lt;channel&gt;.&lt;ev&gt;" (e.g.
        /// "playermovement.room_enter") and the properties are the line's
        /// fields: the session export (admin) writes them as that channel's
        /// lines. See <see cref="ApmEvent"/>. Any thread; nothing is
        /// allocated when the SDK is not collecting.
        /// </summary>
        public static ApmEvent Event(string name)
        {
            var custom = s_custom;
            if (custom == null || !custom.IsActive)
                return default;
            return EventBuilder.Begin(custom, name);
        }

        private static bool TrackEventInternal(string name, string[] pairs, int length)
        {
            try
            {
                var custom = s_custom;
                if (custom == null || !custom.IsActive)
                    return false;
                return custom.TrackEvent(name, CustomCollector.ValidateProps(pairs, length));
            }
            catch (Exception ex)
            {
                ReportInternalError("TrackEvent", ex);
                return false;
            }
        }

        /// <summary>
        /// Records one sample of a custom metric (a duration, a count, a size),
        /// with the current game context. Samples are aggregated on the device
        /// per name and context over a minute (count, sum, min, max and a
        /// histogram), so the server shows p50/p95/p99 per name and context
        /// value without one row per sample. No allocation once the name has
        /// been used; any thread. NaN and infinities are refused.
        /// </summary>
        public static bool RecordMetric(string name, double value)
        {
            return RecordMetric(name, value, null);
        }

        /// <summary>
        /// RecordMetric with a unit shown in the admin ("ms", "bytes", "count";
        /// up to 16 printable ASCII characters). The first sample of a name in a
        /// minute sets it.
        /// </summary>
        public static bool RecordMetric(string name, double value, string unit)
        {
            try
            {
                var custom = s_custom;
                return custom != null && custom.RecordMetric(name, value, unit);
            }
            catch (Exception ex)
            {
                ReportInternalError("RecordMetric", ex);
                return false;
            }
        }

        /// <summary>
        /// Starts a timer for the metric name (e.g. "room_switch_ms"). Stop it
        /// with StopTimer(timer), or, where the handle is not at hand, with
        /// StopTimer(name): one pending timer per name (a second StartTimer of
        /// the same name replaces the first). The elapsed milliseconds are
        /// recorded as a sample of the metric (unit "ms") with the game context
        /// at the time it is stopped: set the context (e.g. the new roomId)
        /// before stopping. A timer that spans a pause of the app is not
        /// recorded. No allocation; any thread; works before Initialize.
        /// </summary>
        public static ApmTimer StartTimer(string name)
        {
            try
            {
                return TimerRegistry.Start(name);
            }
            catch (Exception ex)
            {
                ReportInternalError("StartTimer", ex);
                return default;
            }
        }

        /// <summary>
        /// Stops a timer and records its elapsed time. Returns the elapsed
        /// milliseconds (also when it is not recorded: no consent, the app was
        /// paused meanwhile), or -1 for an invalid handle.
        /// </summary>
        public static double StopTimer(ApmTimer timer)
        {
            try
            {
                if (!timer.IsValid)
                    return -1;
                double ms = TimerRegistry.TicksToMs(System.Diagnostics.Stopwatch.GetTimestamp() - timer.StartTicks);
                TimerRegistry.Forget(timer);
                RecordTimer(timer, ms);
                return ms;
            }
            catch (Exception ex)
            {
                ReportInternalError("StopTimer", ex);
                return -1;
            }
        }

        /// <summary>
        /// Stops the pending timer started with StartTimer(name). Returns the
        /// elapsed milliseconds, or -1 if no timer of that name is pending.
        /// </summary>
        public static double StopTimer(string name)
        {
            try
            {
                if (!TimerRegistry.TryTake(name, out var timer))
                    return -1;
                double ms = TimerRegistry.TicksToMs(System.Diagnostics.Stopwatch.GetTimestamp() - timer.StartTicks);
                RecordTimer(timer, ms);
                return ms;
            }
            catch (Exception ex)
            {
                ReportInternalError("StopTimer", ex);
                return -1;
            }
        }

        /// <summary>Forgets the pending timer of a name without recording it. True if there was one.</summary>
        public static bool CancelTimer(string name)
        {
            try
            {
                return TimerRegistry.Cancel(name);
            }
            catch (Exception ex)
            {
                ReportInternalError("CancelTimer", ex);
                return false;
            }
        }

        private static void RecordTimer(ApmTimer timer, double ms)
        {
            if (timer.PauseEpoch != TimerRegistry.PauseEpoch)
            {
                TimerRegistry.CountSpanningPause();
                return;
            }
            var custom = s_custom;
            if (custom != null)
                custom.RecordMetric(timer.Name, ms, "ms");
        }

        /// <summary>
        /// Writes pending events to disk and uploads them as soon as possible.
        /// Does not block.
        /// </summary>
        public static void Flush()
        {
            if (!_isInitialized)
                return;

            try
            {
                var client = Instance;
                client._exceptionCollector?.FlushPending();
                client._customCollector?.FlushPending();
                client._offlineStorage?.Flush();
                client._reporter?.NotifyGameReady();
                client._reporter?.RetryNow();
            }
            catch (Exception ex)
            {
                ReportInternalError("Flush", ex);
            }
        }

        /// <summary>
        /// Gets recent network log entries (in-memory cache)
        /// </summary>
        public static List<NetworkLogEntry> GetNetworkLogs()
        {
            if (_isInitialized && Instance._networkLogger != null)
            {
                return Instance._networkLogger.GetRecentLogs();
            }
            return new List<NetworkLogEntry>();
        }

        /// <summary>
        /// Gets the network log file path
        /// </summary>
        public static string GetNetworkLogFilePath()
        {
            if (_isInitialized && Instance._networkLogger != null)
            {
                return Instance._networkLogger.GetLogFilePath();
            }
            return null;
        }

        /// <summary>
        /// Clears all network logs
        /// </summary>
        public static void ClearNetworkLogs()
        {
            if (_isInitialized && Instance._networkLogger != null)
            {
                Instance._networkLogger.ClearLogs();
            }
        }

        /// <summary>
        /// Shuts down the SDK
        /// </summary>
        public static void Shutdown()
        {
            if (_isInitialized)
            {
                Instance.ShutdownInternal();
            }
        }

        private void InitializeInternal(ApmConfig config)
        {
            _config = config;
            _logConfig = config;

            if (!_config.Enabled)
            {
                Log(LogLevel.Info, "APM SDK disabled by configuration");
                return;
            }

            // Consent first (PlayerPrefs reads; a write only for a SetConsent made
            // before Initialize): everything below obeys it.
            _config.SetConsentState(ConsentCategory.Technical, ResolveConsent(_config, ConsentCategory.Technical));
            _config.SetConsentState(ConsentCategory.Analytics, ResolveConsent(_config, ConsentCategory.Analytics));
            StoreExplicitConsent();

            Log(LogLevel.Info, $"Initializing APM SDK v{SdkVersion} for {config.AppVersion}");

            // Initialize core components. None of these read or parse stored
            // events on this thread; the storage worker does that in the background.
            _sessionManager = new SessionManager(() => _config.TechnicalAllowed);
            _platform = DeviceInfo.GetPlatformString();
            _deviceModel = DeviceInfo.GetDeviceModel();
            _osVersion = DeviceInfo.GetOSVersion();
            _buildType = DeviceInfo.BuildTypeName(config.BuildType, Application.isEditor, Debug.isDebugBuild);
            _gpu = DeviceInfo.GetGPU();
            _ramClass = DeviceInfo.GetRAMClass();
            _netType = DeviceInfo.GetNetworkType();
            _nextNetTypeCheck = Time.realtimeSinceStartup + NetTypePollSeconds;
            _uploadsEnabled = !Application.isEditor || config.UploadInEditor;
            _offlineStorage = StoragePathForTests != null
                ? new OfflineStorage(config, StoragePathForTests, false)
                : new OfflineStorage(config);
            _networkLogger = new NetworkLogger(config);
            _reporter = new EventReporter(config, _offlineStorage, _networkLogger);
            _crashDir = CrashDirForTests ?? System.IO.Path.Combine(Application.persistentDataPath, CrashJournal.DirectoryName);
            if (_resetDeviceIdBeforeInit)
            {
                // ResetDeviceId before Initialize: stored events carry the old id.
                _resetDeviceIdBeforeInit = false;
                _offlineStorage.Clear(true);
                CrashMonitor.DeleteDirectory(_crashDir);
            }
            if (_config.GetConsent(ConsentCategory.Technical) == ConsentState.Denied)
            {
                // Nothing may stay on the device while denied (files left by a
                // crash right after SetConsent(Denied), or by an older SDK).
                DeleteLocalTechnicalData();
            }
            _offlineStorage.Start();

            // The game clock events carry (APM-10): this is the main thread.
            GameClock.Init();

            // Initialize collectors
            _timeline = new Timeline();
            s_timeline = _timeline;
            _currentScene = SceneManager.GetActiveScene().name;
            CreateCrashMonitor();
            CreateMemorySampler();
            InitializeCollectors();
            RegisterCountCheckpoints();

            // Subscribe to scene changes
            SceneManager.sceneLoaded += OnSceneLoaded;

            _isInitialized = true;
            Log(LogLevel.Info, $"APM SDK initialized successfully (build type {_buildType}, consent technical {_config.GetConsent(ConsentCategory.Technical)}, analytics {_config.GetConsent(ConsentCategory.Analytics)})");
            if (!_uploadsEnabled)
                Log(LogLevel.Info, "Running in the Editor: events are stored but not uploaded (ApmConfig.UploadInEditor)");
        }

        /// <summary>
        /// Crash reporting (APM-7): on the Android player unless
        /// ApmConfig.EnableCrashReporting is off. A few JNI calls here (the
        /// application context and the pid); the journal is written and earlier
        /// processes are reported on their own threads once collecting starts.
        /// </summary>
        private void CreateCrashMonitor()
        {
            if (!_config.EnableCrashReporting)
                return;
            IExitInfoSource source;
            int pid;
            string unityTombstones;
            IMainThreadProbe probe = null;
            if (ExitInfoForTests != null || CrashDirForTests != null)
            {
                source = ExitInfoForTests;
                pid = PidForTests;
                unityTombstones = UnityTombstoneDirForTests;
                probe = ProbeForTests;
            }
            else if (Application.platform == RuntimePlatform.Android)
            {
                var android = AndroidExitInfo.Capture();
                source = android;
                pid = android.Pid;
                // Unity's crash handler writes tombstone_NN there (main thread only: read here).
                unityTombstones = Application.persistentDataPath;
#if UNITY_ANDROID && !UNITY_EDITOR
                if (_config.EnableAnrWatchdog && android.Context != null)
                    probe = new AndroidMainThreadProbe(android.Context, pid);
#endif
            }
            else
            {
                return; // iOS and desktop: not yet (the journal alone would report kills as "unknown")
            }
            var inputs = new JournalInputs
            {
                Pid = pid,
                ProcessStartMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds(),
                AppVersion = _config.AppVersion ?? "",
                BuildType = _buildType ?? "",
                Platform = _platform ?? "",
                DeviceModel = _deviceModel ?? "",
                OsVersion = _osVersion ?? "",
                Gpu = _gpu ?? "",
                RamClass = _ramClass ?? "",
                Abi = AndroidExitInfo.ProcessAbi(),
                NetType = _netType ?? "",
                Scene = _currentScene ?? "",
                Clock = _sessionManager.Clock,
                DeviceId = () => _sessionManager.DeviceId,
                Timeline = _timeline,
                UnityTombstoneDir = unityTombstones,
            };
            var storage = _offlineStorage;
            bool threads = StoragePathForTests == null;
            _crash = new CrashMonitor(_crashDir, inputs, source, EnqueuePreviousCrash,
                () => _config.TechnicalAllowed,
                timeoutMs =>
                {
                    storage.Flush();
                    return storage.WaitIdle(timeoutMs);
                },
                threads);
            _crash.Probe = _config.EnableAnrWatchdog ? probe : null;
            _crash.EnqueueRun = EnqueuePreviousRun;
        }

        /// <summary>
        /// Memory readings and game runs (APM-9). The RSS of /proc/self/statm is
        /// read on its own thread on Android; elsewhere the perf sample reads
        /// Unity's System Used Memory instead.
        /// </summary>
        private void CreateMemorySampler()
        {
            _runs = new RunTracker();
            s_runs = _runs;
            _crash?.SetRuns(_runs);
            if (!_config.EnableMemoryBreakdown)
                return;
            string statm = ProcStatmPathForTests ??
                (MemorySourceForTests == null && Application.platform == RuntimePlatform.Android ? ProcStatm.DefaultPath : null);
            var testSource = MemorySourceForTests;
            // Debug.isDebugBuild: true in development players and the Editor (main thread, read once here).
            bool gfx = Debug.isDebugBuild;
            Func<IMemorySource> source = testSource != null
                ? () => testSource
                : (Func<IMemorySource>)(() => new UnityMemorySource(statm == null, gfx));
            int intervalMs = (int)(Math.Max(0.1f, _config.SamplingIntervalSeconds) * 1000f);
            _memory = new MemorySampler(_runs, source, statm, intervalMs);
        }

        /// <summary>
        /// Enqueues a run that ended in this process. Where the crash journal
        /// runs, its event id is derived from the process and the run (the id
        /// the journal's report of the same run would get, should the process
        /// die before the journal is rewritten without the run): the server
        /// keeps one. Any thread.
        /// </summary>
        internal void EnqueueRun(RunEvent evt)
        {
            var crash = _crash;
            if (crash != null && _sessionManager != null)
            {
                var inputs = crash.Inputs;
                evt.event_id = CrashReporter.EventId(_sessionManager.DeviceId, inputs.Pid, inputs.ProcessStartMs,
                    CrashReporter.RunKind(evt.run_id, evt.run_start_ms));
            }
            EnqueueEvent(evt);
        }

        /// <summary>The perf sample's memory figures (PerformanceCollector, main thread).</summary>
        internal void SampleMemory(PerfSampleEvent evt)
        {
            _memory?.Sample(evt, evt.mem_mb);
        }

        private void InitializeCollectors()
        {
            if (_config.EnablePerformance)
            {
                _performanceCollector = new PerformanceCollector(_config, this);
                _collectors.Add(_performanceCollector);
                _crash?.SetPerf(_performanceCollector);
            }

            if (_config.EnableJankDetection)
            {
                _jankDetector = new JankDetector(_config, this);
                _collectors.Add(_jankDetector);
            }

            _memoryCollector = new MemoryCollector(_config, this);
            _collectors.Add(_memoryCollector);

            if (_config.EnableStartupTiming)
            {
                _startupTracker = new StartupTracker(_config, this);
                _collectors.Add(_startupTracker);
            }

            if (_config.EnableSceneLoadTracking)
            {
                _sceneLoadTracker = new SceneLoadTracker(_config, this);
                _collectors.Add(_sceneLoadTracker);
            }

            if (_config.EnableExceptionCapture)
            {
                _exceptionCollector = new ExceptionCollector(_config, this);
                _exceptionCollector.PersistNow = PersistBeforeTermination;
                // Recent warning/error lines onto the time line (APM-6), from
                // the same log callback.
                _exceptionCollector.LogSink = _timeline;
                _collectors.Add(_exceptionCollector);
            }

            if (_config.EnableCustomEvents)
            {
                _customCollector = new CustomCollector(_config, this, _timeline);
                _collectors.Add(_customCollector);
                s_custom = _customCollector;
            }

            _collectorMarkers.Clear();
            foreach (var collector in _collectors)
                _collectorMarkers.Add(new ProfilerMarker("OzxApm.Collector." + collector.GetType().Name));

            // The startup tracker measures this launch whatever the consent (it
            // keeps a few timestamps in memory; its event is dropped at enqueue
            // without consent). The others only run with technical consent.
            if (_startupTracker != null)
            {
                try
                {
                    _startupTracker.Start();
                }
                catch (Exception ex)
                {
                    ReportInternalError("StartupTracker.Start", ex);
                }
            }
            _collecting = false;
            SetCollecting(_config.TechnicalAllowed);
        }

        /// <summary>
        /// Starts or stops every collector but the startup tracker (technical
        /// consent). A stopped collector costs nothing per frame and holds no
        /// pending data: ExceptionCollector.Stop hands its pending repeat counts to
        /// EnqueueEvent, which drops them without consent.
        /// </summary>
        private void SetCollecting(bool on)
        {
            if (_collecting == on)
                return;
            _collecting = on;
            s_collecting = on;
            try
            {
                _crash?.OnCollecting(on);
            }
            catch (Exception ex)
            {
                ReportInternalError("CrashMonitor", ex);
            }
            try
            {
                if (on)
                    _memory?.Start(StoragePathForTests == null);
                else
                    _memory?.Stop();
            }
            catch (Exception ex)
            {
                ReportInternalError("MemorySampler", ex);
            }
            foreach (var collector in _collectors)
            {
                if (ReferenceEquals(collector, _startupTracker))
                    continue;
                try
                {
                    if (on)
                        collector.Start();
                    else
                        collector.Stop();
                }
                catch (Exception ex)
                {
                    ReportInternalError(collector.GetType().Name + (on ? ".Start" : ".Stop"), ex);
                }
            }
        }

        /// <summary>
        /// Current SDK state (queue, local storage, sequence, upload failures,
        /// main-thread cost). Allocation-free; main thread.
        /// </summary>
        public static ApmStats GetStats()
        {
            var stats = new ApmStats
            {
                Initialized = _isInitialized,
                Consent = GetConsent(ConsentCategory.Technical),
                AnalyticsConsent = GetConsent(ConsentCategory.Analytics),
                ContextKeys = GameContext.Count,
                ContextRejected = GameContext.RejectedCount,
                ContextTruncated = GameContext.TruncatedCount
            };
            if (!_isInitialized || ReferenceEquals(_instance, null))
                return stats;

            var client = _instance;
            stats.Collecting = client._collecting;
            if (client._offlineStorage != null)
            {
                stats.PendingEvents = client._offlineStorage.PendingEventCount;
                stats.StoredBytes = client._offlineStorage.GetStorageSize();
                stats.DroppedEvents = client._offlineStorage.DroppedEventCount;
                stats.LastSeq = client._offlineStorage.LastSeq;
                stats.PersistsToDisk = client._offlineStorage.PersistsToDisk;
                stats.HasWorkerThread = client._offlineStorage.HasWorkerThread;
                // APM-24
                stats.CountCheckpoints = client._offlineStorage.CheckpointWrites;
                stats.CountCheckpointBytes = client._offlineStorage.CheckpointLastBytes;
                stats.CountCheckpointsRecovered = client._offlineStorage.CheckpointsRecovered;
                stats.CountCheckpointOmitted = client._offlineStorage.CheckpointEventsOmitted;
            }
            if (client._reporter != null)
            {
                stats.UploadFailures = client._reporter.ConsecutiveFailures;
                stats.LastUploadSendMs = client._reporter.LastSendTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadCompleteMs = client._reporter.LastCompleteTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadStartMs = client._reporter.LastStartTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadDisposeMs = client._reporter.LastDisposeTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
            }
            if (client._exceptionCollector != null)
            {
                client._exceptionCollector.GetStats(out long occurrences, out long events, out long untracked);
                stats.ErrorsSeen = occurrences;
                stats.ErrorEvents = events;
                stats.ErrorsInSummary = untracked;
            }
            if (client._customCollector != null)
            {
                client._customCollector.GetStats(out long tracked, out long aggregated, out long dropped,
                    out long samples, out long samplesDropped);
                stats.CustomEvents = tracked;
                stats.CustomEventsAggregated = aggregated;
                stats.CustomEventsDropped = dropped;
                stats.MetricSamples = samples;
                stats.MetricSamplesDropped = samplesDropped;
            }
            stats.CustomRejected = CustomCollector.RejectedCount;
            if (client._crash != null)
            {
                stats.CrashReporting = true;
                var reporter = client._crash.Reporter;
                if (reporter != null)
                {
                    stats.CrashesReported = reporter.Reported;
                    stats.ExitsNotCrashes = reporter.NotCrashes;
                }
                var journal = client._crash.Journal;
                if (journal != null)
                {
                    stats.CrashJournalWrites = System.Threading.Interlocked.Read(ref journal.Writes);
                    stats.CrashJournalWriteMs = journal.LastWriteTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                }
            }
            if (client._runs != null)
            {
                stats.RunId = client._runs.CurrentRunId;
                stats.RunsEnded = client._runs.RunsEnded;
            }
            if (client._memory != null)
            {
                stats.MemoryBreakdown = true;
                stats.ProcRss = client._memory.ReadsProcRss;
                stats.MemorySampleMs = client._memory.LastSampleTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.RssReadMs = client._memory.LastRssReadTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.RssMb = client._memory.LastRssMb;
                stats.RssReadings = System.Threading.Interlocked.Read(ref client._memory.RssReadings);
            }
            var jank = client._jankDetector;
            if (jank != null)
            {
                stats.JanksDetected = System.Threading.Interlocked.Read(ref jank.Detected);
                stats.JanksSent = System.Threading.Interlocked.Read(ref jank.Sent);
                stats.JanksDropped = System.Threading.Interlocked.Read(ref jank.Dropped);
                stats.JankExpectedFrameMs = jank.ExpectedFrameMs;
                stats.LastJankDurationMs = jank.LastDurationMs;
                stats.LastJankMaxFrameMs = jank.LastMaxFrameMs;
                stats.LastJankFrames = jank.LastFrames;
                stats.LastJankSlowFrames = jank.LastSlowFrames;
                stats.JankUpdateUs = System.Threading.Interlocked.Read(ref jank.LastSampledTicks) * 1e6 / System.Diagnostics.Stopwatch.Frequency;
                stats.JankSelfCheckOverheadUs = System.Threading.Interlocked.Read(ref jank.SelfCheckOverheadTicks) * 1e6 / System.Diagnostics.Stopwatch.Frequency;
            }
            stats.TimersSpanningPause = TimerRegistry.SpanningPauseCount;
            stats.TimersUntracked = TimerRegistry.UntrackedCount;
            if (client._timeline != null)
                stats.LogLinesSeen = client._timeline.LogLinesSeen;
            stats.LastUpdateMs = client._lastUpdateTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
            stats.BuildType = client._buildType;
            stats.UploadsEnabled = client._uploadsEnabled;
            return stats;
        }

        private long _lastUpdateTicks;

        // ---- Test seams (EditMode) ----

        /// <summary>Storage directory for the next Initialize; the worker is then pumped by the test.</summary>
        internal static string StoragePathForTests;
        /// <summary>APM-7: crash journal directory, exit records and pid for the next Initialize (enables crash reporting off Android).</summary>
        internal static string CrashDirForTests;
        internal static IExitInfoSource ExitInfoForTests;
        internal static string UnityTombstoneDirForTests;
        internal static IMainThreadProbe ProbeForTests;
        internal static int PidForTests = 4242;
        internal CrashMonitor CrashMonitorForTests => _crash;
        /// <summary>APM-9: a statm file read instead of /proc/self/statm, and the engine's memory figures, for the next Initialize.</summary>
        internal static string ProcStatmPathForTests;
        internal static IMemorySource MemorySourceForTests;
        internal MemorySampler MemorySamplerForTests => _memory;
        internal RunTracker RunsForTests => _runs;
        internal OfflineStorage StorageForTests => _offlineStorage;
        internal EventReporter ReporterForTests => _reporter;
        internal ExceptionCollector ExceptionCollectorForTests => _exceptionCollector;
        internal CustomCollector CustomCollectorForTests => _customCollector;
        internal JankDetector JankDetectorForTests => _jankDetector;
        internal void UpdateForTests() => Update();
        internal void PauseForTests(bool paused) => OnApplicationPause(paused);

        /// <summary>Destroys the client and forgets every static state (consent calls, shutdown flag).</summary>
        internal static void ResetForTests()
        {
            var instance = _instance;
            if (!ReferenceEquals(instance, null))
            {
                instance.ShutdownInternal();
                if (instance != null)
                    DestroyImmediate(instance.gameObject);
            }
            _instance = null;
            _isInitialized = false;
            _isShuttingDown = false;
            _logConfig = null;
            s_custom = null;
            GameClock.ResetForTests();
            s_timeline = null;
            s_collecting = false;
            s_runs = null;
            ProcStatmPathForTests = null;
            MemorySourceForTests = null;
            _resetDeviceIdBeforeInit = false;
            for (int i = 0; i < _explicitConsent.Length; i++)
                _explicitConsent[i] = -1;
            StoragePathForTests = null;
            CrashDirForTests = null;
            ExitInfoForTests = null;
            UnityTombstoneDirForTests = null;
            ProbeForTests = null;
            PidForTests = 4242;
        }

        private void Update()
        {
            if (!_isInitialized || !_config.Enabled)
                return;

            long start = System.Diagnostics.Stopwatch.GetTimestamp();
            UpdateInternal();
            _lastUpdateTicks = System.Diagnostics.Stopwatch.GetTimestamp() - start;
        }

        // Profiler markers (development builds only; compiled out of release players).
        // In their own class, so that ApmClient's type initializer does not call
        // into the engine (Log and ReportInternalError run on any thread, and the
        // SDK's plain C# parts can be tested outside Unity).
        private static class Markers
        {
            internal static readonly ProfilerMarker Collectors = new ProfilerMarker("OzxApm.Collectors");
            internal static readonly ProfilerMarker Upload = new ProfilerMarker("OzxApm.Upload");
            internal static readonly ProfilerMarker Enqueue = new ProfilerMarker("OzxApm.Enqueue");
        }
        private readonly List<ProfilerMarker> _collectorMarkers = new List<ProfilerMarker>();

        private void UpdateInternal()
        {
            if (!_collecting)
            {
                // No technical consent: no collector, no upload. Only the startup
                // tracker counts its first frames, and without a worker thread the
                // storage is pumped (it may have a Clear to carry out). No allocation.
                try
                {
                    _startupTracker?.Update();
                    _offlineStorage.Tick();
                }
                catch (Exception ex)
                {
                    ReportInternalError("Update", ex);
                }
                return;
            }

            // Update collectors
            using (Markers.Collectors.Auto())
            {
                for (int i = 0; i < _collectors.Count; i++)
                {
                    try
                    {
                        using (_collectorMarkers[i].Auto())
                            _collectors[i].Update();
                    }
                    catch (Exception ex)
                    {
                        ReportInternalError("Collector.Update", ex);
                    }
                }
            }

            float now = Time.realtimeSinceStartup;
            if (now >= _nextNetTypeCheck)
            {
                _nextNetTypeCheck = now + NetTypePollSeconds;
                _netType = DeviceInfo.GetNetworkType();
                _crash?.OnNetType(_netType);
            }

            using (Markers.Upload.Auto())
            {
                try
                {
                    _offlineStorage.Tick();
                    if (_uploadsEnabled)
                        _reporter.Update();
                }
                catch (Exception ex)
                {
                    ReportInternalError("Reporter.Update", ex);
                }
            }
        }

        private void OnApplicationPause(bool paused)
        {
            if (!_isInitialized)
                return;

            try
            {
                _sessionManager.OnApplicationPause(paused);
                // The journal says whether a later kill hit the game in the foreground (APM-7).
                _crash?.OnPause(paused);
                // The RSS reader sleeps in the background (APM-9).
                if (_memory != null)
                    _memory.Paused = paused;
                // A jank in progress is sent before the flush below; the frame
                // after resuming holds the time in the background (APM-21).
                _jankDetector?.OnPause(paused);

                if (paused)
                {
                    // Timers running now would include the time in background.
                    TimerRegistry.OnPause();
                    // The OS may kill us in the background: get everything onto disk
                    // and sealed for upload. Bounded wait; the worker is normally idle.
                    _exceptionCollector?.FlushPending();
                    _customCollector?.FlushPending();
                    _offlineStorage.Flush();
                    _offlineStorage.WaitIdle(PauseFlushTimeoutMs);
                }
                else
                {
                    // The network may have changed while we were away.
                    _netType = DeviceInfo.GetNetworkType();
                    _reporter.RetryNow();
                }
            }
            catch (Exception ex)
            {
                ReportInternalError("OnApplicationPause", ex);
            }
        }

        private void OnApplicationFocus(bool hasFocus)
        {
            if (!_isInitialized)
                return;

            try
            {
                _sessionManager.OnApplicationFocus(hasFocus);
                _jankDetector?.OnFocus(hasFocus);
            }
            catch (Exception ex)
            {
                ReportInternalError("OnApplicationFocus", ex);
            }
        }

        private void OnApplicationQuit()
        {
            ShutdownInternal();
        }

        private void OnDestroy()
        {
            _isShuttingDown = true;
            ShutdownInternal();
        }

        private void ShutdownInternal()
        {
            if (!_isInitialized || _isStopping)
                return;

            // OnApplicationQuit and OnDestroy both land here; run once.
            _isStopping = true;

            try
            {
                Log(LogLevel.Info, "Shutting down APM SDK");

                // A run still open ends here (APM-9), before the journal is
                // marked clean (a clean journal's run would be lost).
                var openRun = _runs?.End(RunEvent.EndShutdown, null);
                if (openRun != null)
                    EnqueueRun(openRun);
                _memory?.Dispose();

                // Stop collectors (ExceptionCollector enqueues its pending repeats here)
                foreach (var collector in _collectors)
                {
                    try
                    {
                        collector.Stop();
                    }
                    catch (Exception ex)
                    {
                        ReportInternalError(collector.GetType().Name + ".Stop", ex);
                    }
                }
                _collectors.Clear();
                _collecting = false;
                s_collecting = false;
                s_custom = null;
                s_runs = null;

                SceneManager.sceneLoaded -= OnSceneLoaded;

                // A clean exit is not a crash: this process's journal is marked clean
                // (kept until the next launch has seen the exit record: an ANR the
                // process survived is only in its trace; APM-7).
                _crash?.OnShutdown();

                // Unsent batches stay on disk and are uploaded on the next launch.
                _reporter.Shutdown();
                _offlineStorage.Shutdown(ShutdownFlushTimeoutMs);
            }
            catch (Exception ex)
            {
                ReportInternalError("Shutdown", ex);
            }
            finally
            {
                _isInitialized = false;
                _isStopping = false;
            }
        }

        private void OnSceneLoaded(Scene scene, LoadSceneMode mode)
        {
            try
            {
                // Before the scene name changes: a jank in progress belongs to
                // the scene it happened in; the loading frames are not janks.
                _jankDetector?.OnSceneLoaded();
                _currentScene = scene.name;
                _crash?.OnScene(scene.name);
                RecordBreadcrumb($"Scene: {scene.name}");
            }
            catch (Exception ex)
            {
                ReportInternalError("OnSceneLoaded", ex);
            }
        }

        /// <summary>
        /// An unhandled exception is about to end the process (APM-3): write what
        /// is queued to disk before returning, at most TerminationFlushTimeoutMs.
        /// Called on the dying thread, after ExceptionCollector enqueued its counts.
        /// </summary>
        private void PersistBeforeTermination()
        {
            try
            {
                // The current window of custom metrics and counts too (it would
                // otherwise be lost with the process).
                _customCollector?.FlushPending();
                // The journal with the exception on its time line (APM-7): the
                // process ends before the journal's thread would write it.
                _crash?.Journal?.WriteNow();
                var storage = _offlineStorage;
                if (storage == null)
                    return;
                storage.Flush();
                // Without a worker thread WaitIdle pumps on the calling thread,
                // which must stay the main thread's job.
                if (storage.HasWorkerThread)
                    storage.WaitIdle(TerminationFlushTimeoutMs);
            }
            catch
            {
                // Nothing may escape into the dying thread.
            }
        }

        /// <summary>
        /// Enqueues an event. It is written to disk by the storage worker within
        /// PersistIntervalMs (exceptions at once) and kept until the server
        /// confirms it. Any thread (exceptions are reported on the thread that
        /// logged them).
        /// </summary>
        internal void EnqueueEvent(BaseEvent evt)
        {
            if (!_isInitialized || evt == null)
                return;

            // Consent gate (APM-14): without technical consent the event is not
            // even queued. Any thread: one volatile read.
            if (!_config.TechnicalAllowed)
                return;

            try
            {
                // Keep the time the event happened if the collector set it
                // (e.g. first-seen time of an exception); otherwise it is now.
                if (evt.timestamp <= 0)
                    evt.timestamp = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
                // event_id and seq are assigned by the storage worker before the event
                // is persisted (Guid generation reads the OS random source: keep it
                // off the main thread).

                StampCommonFields(evt);
                // The game clock (APM-10) of events enqueued when they happen
                // (perf samples, janks, scene loads, startup, runs). Custom
                // events and exceptions carry the one of their call or first
                // occurrence; counts and aggregates over a window carry none.
                if (evt.frame < 0 && StampsClockAtEnqueue(evt))
                    GameClock.Stamp(evt);

                using (Markers.Enqueue.Auto())
                    _offlineStorage.Enqueue(evt);
            }
            catch (Exception ex)
            {
                ReportInternalError("EnqueueEvent", ex);
            }
        }

        /// <summary>
        /// APM-24: the collectors whose counts wait for their window to close
        /// are checkpointed by the storage worker (OfflineStorage,
        /// ICountCheckpointSource); their copies get the common fields here,
        /// on the worker thread, like a queued event.
        /// </summary>
        private void RegisterCountCheckpoints()
        {
            var storage = _offlineStorage;
            if (storage == null)
                return;
            storage.CheckpointStamp = StampCommonFields;
            if (_exceptionCollector != null)
                storage.AddCheckpointSource(_exceptionCollector);
            if (_customCollector != null)
                storage.AddCheckpointSource(_customCollector);
        }

        /// <summary>
        /// APM-24: a collector counted something new since its last checkpoint
        /// (urgent: it closed a window). Any thread, never under a collector's
        /// lock; at most once per checkpoint interval per collector, plus once
        /// per window close.
        /// </summary>
        internal void RequestCountCheckpoint(bool urgent)
        {
            try
            {
                _offlineStorage?.RequestCheckpoint(urgent);
            }
            catch (Exception ex)
            {
                ReportInternalError("RequestCountCheckpoint", ex);
            }
        }

        /// <summary>
        /// Events whose enqueue is the moment they describe: everything but
        /// custom events and metrics (stamped at the call, or a count or
        /// aggregate over a window) and exceptions (stamped at the first
        /// occurrence they report).
        /// </summary>
        internal static bool StampsClockAtEnqueue(BaseEvent evt)
        {
            return !(evt is CustomEvent) && !(evt is CustomMetricEvent) && !(evt is ExceptionEvent);
        }

        /// <summary>
        /// Enqueues the crash of an earlier process (APM-7, CrashReporter, on its
        /// thread). Unlike EnqueueEvent, every field is kept as it is: the
        /// session, version, scene, device details and context are those of the
        /// crashed process. Only with technical consent.
        /// </summary>
        internal void EnqueuePreviousCrash(CrashEvent evt)
        {
            var storage = _offlineStorage;
            if (evt == null || storage == null || !_config.TechnicalAllowed)
                return;
            try
            {
                if (evt.ctx == null)
                    evt.ctx = ContextSnapshot.Empty;
                storage.Enqueue(evt);
            }
            catch (Exception ex)
            {
                ReportInternalError("EnqueuePreviousCrash", ex);
            }
        }

        /// <summary>
        /// Enqueues the run an earlier process had open when it died (APM-9,
        /// CrashReporter.BuildRun, on its thread): every field is that
        /// process's, like EnqueuePreviousCrash. Only with technical consent.
        /// </summary>
        internal void EnqueuePreviousRun(RunEvent evt)
        {
            var storage = _offlineStorage;
            if (evt == null || storage == null || !_config.TechnicalAllowed)
                return;
            try
            {
                if (evt.ctx == null)
                    evt.ctx = ContextSnapshot.Empty;
                storage.Enqueue(evt);
            }
            catch (Exception ex)
            {
                ReportInternalError("EnqueuePreviousRun", ex);
            }
        }

        /// <summary>
        /// Sets the APM-5 fields of an event: build type, device details and the game
        /// context as it is now (unless the collector captured an earlier one).
        /// Allocation-free while the context is unchanged; any thread.
        /// </summary>
        /// <summary>
        /// The fields every event of this process carries: version, device,
        /// session (unless the collector set the one it happened in), device id,
        /// scene, build type and device details, the game context (unless
        /// captured earlier). Any thread (EnqueueEvent; the storage worker for
        /// count checkpoints, APM-24).
        /// </summary>
        private void StampCommonFields(BaseEvent evt)
        {
            evt.app_version = _config.AppVersion;
            evt.platform = _platform;
            evt.device_model = _deviceModel;
            evt.os_version = _osVersion;
            // A collector may have set the session the event happened in
            // (exception counts closed after a new session started).
            if (string.IsNullOrEmpty(evt.session_id))
                evt.session_id = _sessionManager.SessionId;
            evt.device_id = _sessionManager.DeviceId;
            evt.scene = _currentScene;
            StampContext(evt, _buildType, _gpu, _ramClass, _netType);
        }

        internal static void StampContext(BaseEvent evt, string buildType, string gpu, string ramClass, string netType)
        {
            evt.build_type = buildType;
            evt.gpu = gpu;
            evt.ram_class = ramClass;
            evt.net_type = netType;
            if (evt.ctx == null)
                evt.ctx = GameContext.Current;
        }

        /// <summary>
        /// Gets the current scene name
        /// </summary>
        internal string GetCurrentScene()
        {
            return _currentScene;
        }

        /// <summary>
        /// Gets memory stats from the memory collector
        /// </summary>
        /// <summary>
        /// Managed heap size in MB. Uses the value MemoryCollector already read this
        /// frame instead of taking the GC lock again.
        /// </summary>
        internal float GetUsedMemoryMB()
        {
            return _memoryCollector != null ? _memoryCollector.LastUsedMemoryMB : DeviceInfo.GetUsedMemoryMB();
        }

        internal (int gcCount, float gcAllocKb) GetMemoryStats()
        {
            if (_memoryCollector != null)
            {
                return _memoryCollector.GetRecentStats();
            }
            return (0, 0);
        }

        /// <summary>
        /// Logs an SDK-internal exception at warning level (rate limited). Never rethrows.
        /// </summary>
        internal static void ReportInternalError(string where, Exception ex)
        {
            if (System.Threading.Interlocked.Increment(ref _internalErrorCount) > MaxReportedInternalErrors)
                return;
            Log(LogLevel.Warning, $"Internal error in {where}: {ex.GetType().Name}: {ex.Message}");
        }

        /// <summary>Whether Log(level, ...) would write: lets a caller skip formatting the message.</summary>
        internal static bool IsLogEnabled(LogLevel level)
        {
            var config = _logConfig;
            return config != null && level != LogLevel.None && level <= config.LogLevel;
        }

        /// <summary>
        /// SDK-internal logging. Thread-safe. Never uses Debug.LogError or
        /// Debug.LogException: those would be captured by ExceptionCollector (and the
        /// game's own error reporting) and make the SDK report itself. Errors are
        /// written as warnings.
        /// </summary>
        internal static void Log(LogLevel level, string message)
        {
            var config = _logConfig;
            if (config == null || level == LogLevel.None || level > config.LogLevel)
                return;

            try
            {
                string text = LogPrefix + " " + message;
                switch (level)
                {
                    case LogLevel.Error:
                    case LogLevel.Warning:
                        Debug.LogWarning(text);
                        break;
                    default:
                        Debug.Log(text);
                        break;
                }
            }
            catch
            {
                // Logging must never throw into the game.
            }
        }
    }
}
