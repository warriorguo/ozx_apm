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
        private static ApmClient _instance;
        private static readonly object _lock = new object();
        private static bool _isInitialized;
        private static bool _isShuttingDown;

        private ApmConfig _config;
        private SessionManager _sessionManager;
        private EventQueue _eventQueue;
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
        /// Initializes the APM SDK with the given configuration
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

            Instance.InitializeInternal(config);
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
                Instance._startupTracker.MarkTTI();
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
        /// Flushes all pending events
        /// </summary>
        public static void Flush()
        {
            if (_isInitialized)
            {
                Instance._eventQueue?.Flush();
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

            if (!_config.Enabled)
            {
                Log(LogLevel.Info, "APM SDK disabled by configuration");
                return;
            }

            Log(LogLevel.Info, $"Initializing APM SDK v1.0.0 for {config.AppVersion}");

            // Initialize core components
            _sessionManager = new SessionManager();
            _eventQueue = new EventQueue(config);
            _offlineStorage = new OfflineStorage(config);
            _networkLogger = new NetworkLogger(config);
            _reporter = new EventReporter(config, _offlineStorage, _networkLogger);

            // Subscribe to batch ready events
            _eventQueue.OnBatchReady += OnBatchReady;

            // Initialize collectors
            InitializeCollectors();

            // Subscribe to scene changes
            SceneManager.sceneLoaded += OnSceneLoaded;
            _currentScene = SceneManager.GetActiveScene().name;

            // Process any offline events from previous sessions
            _offlineStorage.ProcessOfflineEvents(_reporter);

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
                collector.Start();
            }
        }

        private void Update()
        {
            if (!_isInitialized || !_config.Enabled)
                return;

            // Update collectors
            foreach (var collector in _collectors)
            {
                collector.Update();
            }

            // Update event queue
            _eventQueue.Update();
        }

        private void OnApplicationPause(bool paused)
        {
            if (!_isInitialized)
                return;

            _sessionManager.OnApplicationPause(paused);

            if (paused)
            {
                // Flush events when going to background
                _eventQueue.Flush();
            }
        }

        private void OnApplicationFocus(bool hasFocus)
        {
            if (!_isInitialized)
                return;

            _sessionManager.OnApplicationFocus(hasFocus);
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
            if (!_isInitialized)
                return;

            Log(LogLevel.Info, "Shutting down APM SDK");

            // Stop collectors
            foreach (var collector in _collectors)
            {
                collector.Stop();
            }
            _collectors.Clear();

            // Flush remaining events
            _eventQueue.Flush();

            // Unsubscribe from events
            _eventQueue.OnBatchReady -= OnBatchReady;
            SceneManager.sceneLoaded -= OnSceneLoaded;

            _isInitialized = false;
        }

        private void OnSceneLoaded(Scene scene, LoadSceneMode mode)
        {
            _currentScene = scene.name;
            RecordBreadcrumb($"Scene: {scene.name}");
        }

        private void OnBatchReady(List<BaseEvent> batch)
        {
            _reporter.SendBatch(batch);
        }

        /// <summary>
        /// Enqueues an event for transmission
        /// </summary>
        internal void EnqueueEvent(BaseEvent evt)
        {
            if (!_isInitialized || evt == null)
                return;

            // Fill common fields
            evt.timestamp = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
            evt.app_version = _config.AppVersion;
            evt.platform = DeviceInfo.GetPlatformString();
            evt.device_model = DeviceInfo.GetDeviceModel();
            evt.os_version = DeviceInfo.GetOSVersion();
            evt.session_id = _sessionManager.SessionId;
            evt.device_id = _sessionManager.DeviceId;
            evt.scene = _currentScene;

            _eventQueue.Enqueue(evt);
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

        internal static void Log(LogLevel level, string message)
        {
            if (_instance == null || _instance._config == null)
                return;

            if (level > _instance._config.LogLevel)
                return;

            string prefix = "[OzxApm] ";
            switch (level)
            {
                case LogLevel.Error:
                    Debug.LogError(prefix + message);
                    break;
                case LogLevel.Warning:
                    Debug.LogWarning(prefix + message);
                    break;
                case LogLevel.Info:
                case LogLevel.Debug:
                    Debug.Log(prefix + message);
                    break;
            }
        }
    }
}
