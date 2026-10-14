using System;
using System.Collections.Generic;
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
        private static ConsentState? _consentBeforeInit;
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

        /// <summary>
        /// Current consent state. Events are only collected, stored and uploaded when Granted.
        /// </summary>
        public static ConsentState Consent
        {
            get
            {
                if (_isInitialized && _instance != null && _instance._config != null)
                    return _instance._config.Consent;
                return _consentBeforeInit ?? ConsentState.Granted;
            }
        }

        /// <summary>
        /// Sets the player's consent. Takes effect immediately: while not Granted,
        /// nothing is enqueued, written to disk or uploaded. Can be called before
        /// Initialize. Persisting the choice and deleting already stored data on
        /// Denied are part of APM-14.
        /// </summary>
        public static void SetConsent(ConsentState state)
        {
            _consentBeforeInit = state;
            var config = _logConfig;
            if (config != null)
                config.Consent = state;
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
        /// Marks Time To Interactive
        /// </summary>
        public static void MarkTTI()
        {
            if (_isInitialized && Instance._startupTracker != null)
            {
                try
                {
                    Instance._startupTracker.MarkTTI();
                }
                catch (Exception ex)
                {
                    ReportInternalError("MarkTTI", ex);
                }
            }
        }

        /// <summary>
        /// Records a custom breadcrumb for crash context
        /// </summary>
        public static void RecordBreadcrumb(string message)
        {
            if (_isInitialized && Instance._exceptionCollector != null)
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
            if (_consentBeforeInit.HasValue)
                _config.Consent = _consentBeforeInit.Value;
            _logConfig = config;

            if (!_config.Enabled)
            {
                Log(LogLevel.Info, "APM SDK disabled by configuration");
                return;
            }

            Log(LogLevel.Info, $"Initializing APM SDK v1.0.0 for {config.AppVersion}");

            // Initialize core components. None of these read or parse stored
            // events on this thread; the storage worker does that in the background.
            _sessionManager = new SessionManager();
            _offlineStorage = new OfflineStorage(config);
            _networkLogger = new NetworkLogger(config);
            _reporter = new EventReporter(config, _offlineStorage, _networkLogger);
            _offlineStorage.Start();

            // Initialize collectors
            InitializeCollectors();

            // Subscribe to scene changes
            SceneManager.sceneLoaded += OnSceneLoaded;
            _currentScene = SceneManager.GetActiveScene().name;

            _isInitialized = true;
            Log(LogLevel.Info, "APM SDK initialized successfully");
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

            // Start all collectors
            foreach (var collector in _collectors)
            {
                try
                {
                    collector.Start();
                }
                catch (Exception ex)
                {
                    ReportInternalError(collector.GetType().Name + ".Start", ex);
                }
            }
        }

        private void Update()
        {
            if (!_isInitialized || !_config.Enabled)
                return;

            // Update collectors
            for (int i = 0; i < _collectors.Count; i++)
            {
                try
                {
                    _collectors[i].Update();
                }
                catch (Exception ex)
                {
                    ReportInternalError("Collector.Update", ex);
                }
            }

            try
            {
                _offlineStorage.Tick();
                _reporter.Update();
            }
            catch (Exception ex)
            {
                ReportInternalError("Reporter.Update", ex);
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

            // Consent gate: without consent the event is not even queued.
            if (_config.Consent != ConsentState.Granted)
                return;

            try
            {
                // Keep the time the event happened if the collector set it
                // (e.g. first-seen time of an exception); otherwise it is now.
                if (evt.timestamp <= 0)
                    evt.timestamp = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
                if (string.IsNullOrEmpty(evt.event_id))
                    evt.event_id = Guid.NewGuid().ToString("N");
                // evt.seq is assigned by the storage worker, in enqueue order.

                evt.app_version = _config.AppVersion;
                evt.platform = DeviceInfo.GetPlatformString();
                evt.device_model = DeviceInfo.GetDeviceModel();
                evt.os_version = DeviceInfo.GetOSVersion();
                evt.session_id = _sessionManager.SessionId;
                evt.device_id = _sessionManager.DeviceId;
                evt.scene = _currentScene;

                _offlineStorage.Enqueue(evt);
            }
            catch (Exception ex)
            {
                ReportInternalError("EnqueueEvent", ex);
            }
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
