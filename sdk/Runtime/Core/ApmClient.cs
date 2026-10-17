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
            _offlineStorage.Clear();
            _networkLogger?.ClearLogs();
            _sessionManager.OnConsentDenied();
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
        /// Records a custom breadcrumb for crash context. Ignored without technical consent.
        /// </summary>
        public static void RecordBreadcrumb(string message)
        {
            if (_isInitialized && Instance._collecting && Instance._exceptionCollector != null)
            {
                Instance._exceptionCollector.RecordBreadcrumb(message);
            }
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
            if (_resetDeviceIdBeforeInit)
            {
                // ResetDeviceId before Initialize: stored events carry the old id.
                _resetDeviceIdBeforeInit = false;
                _offlineStorage.Clear(true);
            }
            if (_config.GetConsent(ConsentCategory.Technical) == ConsentState.Denied)
            {
                // Nothing may stay on the device while denied (files left by a
                // crash right after SetConsent(Denied), or by an older SDK).
                DeleteLocalTechnicalData();
            }
            _offlineStorage.Start();

            // Initialize collectors
            InitializeCollectors();

            // Subscribe to scene changes
            SceneManager.sceneLoaded += OnSceneLoaded;
            _currentScene = SceneManager.GetActiveScene().name;

            _isInitialized = true;
            Log(LogLevel.Info, $"APM SDK initialized successfully (build type {_buildType}, consent technical {_config.GetConsent(ConsentCategory.Technical)}, analytics {_config.GetConsent(ConsentCategory.Analytics)})");
            if (!_uploadsEnabled)
                Log(LogLevel.Info, "Running in the Editor: events are stored but not uploaded (ApmConfig.UploadInEditor)");
        }

        private void InitializeCollectors()
        {
            if (_config.EnablePerformance)
            {
                _performanceCollector = new PerformanceCollector(_config, this);
                _collectors.Add(_performanceCollector);
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
                _collectors.Add(_exceptionCollector);
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
            }
            if (client._reporter != null)
            {
                stats.UploadFailures = client._reporter.ConsecutiveFailures;
                stats.LastUploadSendMs = client._reporter.LastSendTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadCompleteMs = client._reporter.LastCompleteTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadStartMs = client._reporter.LastStartTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
                stats.LastUploadDisposeMs = client._reporter.LastDisposeTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
            }
            stats.LastUpdateMs = client._lastUpdateTicks * 1000.0 / System.Diagnostics.Stopwatch.Frequency;
            stats.BuildType = client._buildType;
            stats.UploadsEnabled = client._uploadsEnabled;
            return stats;
        }

        private long _lastUpdateTicks;

        // ---- Test seams (EditMode) ----

        /// <summary>Storage directory for the next Initialize; the worker is then pumped by the test.</summary>
        internal static string StoragePathForTests;
        internal OfflineStorage StorageForTests => _offlineStorage;
        internal EventReporter ReporterForTests => _reporter;
        internal void UpdateForTests() => Update();

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
            _resetDeviceIdBeforeInit = false;
            for (int i = 0; i < _explicitConsent.Length; i++)
                _explicitConsent[i] = -1;
            StoragePathForTests = null;
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
        private static readonly ProfilerMarker s_collectorsMarker = new ProfilerMarker("OzxApm.Collectors");
        private static readonly ProfilerMarker s_uploadMarker = new ProfilerMarker("OzxApm.Upload");
        private readonly List<ProfilerMarker> _collectorMarkers = new List<ProfilerMarker>();
        private static readonly ProfilerMarker s_enqueueMarker = new ProfilerMarker("OzxApm.Enqueue");

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
            using (s_collectorsMarker.Auto())
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
            }

            using (s_uploadMarker.Auto())
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

                if (paused)
                {
                    // The OS may kill us in the background: get everything onto disk
                    // and sealed for upload. Bounded wait; the worker is normally idle.
                    _exceptionCollector?.FlushPending();
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

                SceneManager.sceneLoaded -= OnSceneLoaded;

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
                _currentScene = scene.name;
                RecordBreadcrumb($"Scene: {scene.name}");
            }
            catch (Exception ex)
            {
                ReportInternalError("OnSceneLoaded", ex);
            }
        }

        /// <summary>
        /// Enqueues an event. It is written to disk by the storage worker within
        /// PersistIntervalMs and kept until the server confirms it. Main thread.
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

                evt.app_version = _config.AppVersion;
                evt.platform = _platform;
                evt.device_model = _deviceModel;
                evt.os_version = _osVersion;
                evt.session_id = _sessionManager.SessionId;
                evt.device_id = _sessionManager.DeviceId;
                evt.scene = _currentScene;
                StampContext(evt, _buildType, _gpu, _ramClass, _netType);

                using (s_enqueueMarker.Auto())
                    _offlineStorage.Enqueue(evt);
            }
            catch (Exception ex)
            {
                ReportInternalError("EnqueueEvent", ex);
            }
        }

        /// <summary>
        /// Sets the APM-5 fields of an event: build type, device details and the game
        /// context as it is now (unless the collector captured an earlier one).
        /// Allocation-free while the context is unchanged; any thread.
        /// </summary>
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
